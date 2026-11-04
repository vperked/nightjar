// Package diagnose reads log lines and says, in plain words, what went wrong and what to do.
//
// It works in two layers. Rules recognise errors with a known cause (a port already in use, a
// missing file, a full disk) and pull out the detail that matters: the port, the path, the host.
// Every other error-looking line is reduced to its shape (numbers, paths, addresses and quoted
// values masked) and grouped, so a message repeated a hundred times reads as one finding with a
// count. Root causes rank above their symptoms: "port in use" comes before "systemd gave up".
package diagnose

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Finding is one problem found in the logs.
type Finding struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Explain  string `json:"explain"`
	Fix      string `json:"fix,omitempty"`
	Severity string `json:"severity"` // critical, warning
	// Line is the latest log line that shows it.
	Line  string `json:"line,omitempty"`
	Count int    `json:"count"`
	// Known is true for a recognised cause, false for a repeated error grouped by its shape.
	Known bool `json:"known"`

	rank int
	key  string
	last int
}

// Context is what else is known about the program the lines come from.
type Context struct {
	// Result and ExitStatus are systemd's: why the service last stopped.
	Result     string
	ExitStatus int
}

// maxFindings caps what one diagnosis returns.
const maxFindings = 5

// Lines diagnoses log lines, oldest first. The strongest likely cause comes first.
func Lines(lines []string, c Context) []Finding {
	found := map[string]*Finding{}
	add := func(f Finding, i int) {
		if prev, ok := found[f.key]; ok {
			prev.Count++
			prev.Line, prev.last = f.Line, i
			return
		}
		f.Count, f.last = 1, i
		found[f.key] = &f
	}
	for i, raw := range lines {
		msg := Strip(raw)
		if msg == "" {
			continue
		}
		if f, ok := match(msg); ok {
			add(f, i)
			continue
		}
		if looksLikeError(msg) {
			add(group(msg), i)
		}
	}
	if f, ok := exit(c); ok {
		if _, dup := found[f.key]; !dup {
			f.Count, f.last = 1, len(lines)
			found[f.key] = &f
		}
	}
	out := make([]Finding, 0, len(found))
	for _, f := range found {
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b Finding) int {
		return cmp.Or(cmp.Compare(b.rank, a.rank), cmp.Compare(b.Count, a.Count), cmp.Compare(b.last, a.last))
	})
	for i := range out {
		if !out[i].Known {
			out[i].Explain = "An error the logs repeat, seen " + times(out[i].Count) + " in these lines. The latest is below."
		}
	}
	if len(out) > maxFindings {
		out = out[:maxFindings]
	}
	return out
}

// Text diagnoses one message, such as a failed check's detail. It reports only recognised causes.
func Text(msg string) (Finding, bool) {
	f, ok := match(Strip(msg))
	if ok {
		f.Count = 1
	}
	return f, ok
}

// journalPrefix is journalctl's short-iso prefix: time, host, and unit[pid].
var journalPrefix = regexp.MustCompile(`^\d{4}-\d\d-\d\dT[\d:.]+(?:[+-]\d{2}:?\d{2}|Z)?\s+\S+\s+[^:\s]+(?:\[\d+\])?:\s*`)

// Strip removes the journal's time, host and process prefix, leaving the message.
func Strip(line string) string {
	return strings.TrimSpace(journalPrefix.ReplaceAllString(strings.TrimRight(line, "\r"), ""))
}

// ---------- recognised causes ----------

type rule struct {
	id       string
	rank     int // higher is more likely the root cause
	severity string
	re       *regexp.Regexp
	// build fills in the finding from the matching line; it may refine or reject the match.
	build func(line string) (title, explain, fix string, ok bool)
}

func fixed(title, explain, fix string) func(string) (string, string, string, bool) {
	return func(string) (string, string, string, bool) { return title, explain, fix, true }
}

var (
	portRe = regexp.MustCompile(`(?:(?:\d{1,3}(?:\.\d{1,3}){3}|\]|localhost|\*|::|0\.0\.0\.0)?:|\bport\s+)(\d{2,5})\b`)
	pathRe = regexp.MustCompile(`(?:^|[\s'"(\[=])(/(?:[\w.@+~-]+/?)+)`)
	addrRe = regexp.MustCompile(`\b((?:\d{1,3}(?:\.\d{1,3}){3}|localhost|\[[0-9a-f:]+\]|[a-z0-9-]+(?:\.[a-z0-9-]+)+)(?::\d{2,5})?)\b`)
	hostRe = regexp.MustCompile(`(?i)(?:lookup|host|resolve(?: host)?|getaddrinfo \w+)[: ]+'?([a-z0-9-]+(?:\.[a-z0-9-]+)+)`)
	quoted = regexp.MustCompile(`'([^']+)'`)

	moduleRe    = regexp.MustCompile(`No module named '?([\w.]+)`)
	exceptionRe = regexp.MustCompile(`([A-Z][\w.]*(?:Error|Exception)): (.+)`)
	httpRe      = regexp.MustCompile(`\bHTTP[/ ]?(?:\d\.\d )?(\d{3})\b`)
)

func first(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// path is the first absolute path in the line that is not just "/".
func path(s string) string {
	for _, m := range pathRe.FindAllStringSubmatch(s, -1) {
		p := strings.TrimRight(m[1], ".:,")
		if p != "/" && !strings.HasPrefix(p, "//") {
			return p
		}
	}
	return ""
}

func or(v, fallback string) string { return cmp.Or(v, fallback) }

// named is ": " and the name, or nothing when there is none.
func named(v string) string {
	if v == "" {
		return ""
	}
	return ": " + v
}

var rules = []rule{
	{"privileged_port", 95, "critical", regexp.MustCompile(`(?i)(?:bind|listen).*permission denied|permission denied.*(?:bind|listen)`), func(l string) (string, string, string, bool) {
		port, _ := strconv.Atoi(first(portRe, l))
		if port == 0 || port >= 1024 {
			return "", "", "", false
		}
		return "Port " + strconv.Itoa(port) + " needs root",
			"Ports below 1024 can only be opened by root, and this program runs as a normal user.",
			"Use a port of 1024 or above in its settings, or let it bind low ports with `AmbientCapabilities=CAP_NET_BIND_SERVICE` in its service.", true
	}},
	{"port_in_use", 100, "critical", regexp.MustCompile(`(?i)address already in use|EADDRINUSE|only one usage of each socket address|port \d+ (?:is )?(?:already )?in use|failed to bind.*in use`), func(l string) (string, string, string, bool) {
		port := first(portRe, l)
		if port == "" {
			return "Its port is already in use", "Another program, or a second copy of this one, already holds the port it wants.",
				"Find what holds it with `sudo ss -ltnp`, then stop that program or give this one another port.", true
		}
		return "Port " + port + " is already in use", "Another program, or a second copy of this one, already listens on port " + port + ".",
			"See what holds it with `sudo ss -ltnp 'sport = :" + port + "'`, then stop that program or give this one another port.", true
	}},
	{"disk_full", 98, "critical", regexp.MustCompile(`(?i)no space left on device|ENOSPC|not enough space on the disk|database or disk is full|disk (?:is )?full|insufficient (?:disk )?space`), func(l string) (string, string, string, bool) {
		where := path(l)
		title := "The disk is full"
		if where != "" {
			title = "The disk holding " + where + " is full"
		}
		return title, "It could not write because there is no free space left.",
			"Free some space (the Overview's Storage card shows which disk), then restart it. Nightjar can clear old logs for you under Settings → Disks.", true
	}},
	{"read_only", 97, "critical", regexp.MustCompile(`(?i)read-only file system|EROFS`), fixed("The disk is read-only",
		"The system switched a disk to read-only, which it does when it finds file system errors or a failing drive.",
		"Check `sudo dmesg | tail -50` and the Drive health card. A reboot runs a file system check; replace the drive if it keeps happening.")},
	{"oom", 96, "critical", regexp.MustCompile(`(?i)out of memory|oom[- ]kill|killed process \d+|cannot allocate memory|ENOMEM|OutOfMemoryError|MemoryError\b|heap out of memory|memory exhausted`), fixed("It ran out of memory",
		"The program needed more memory than the machine had free, so it was killed or could not continue.",
		"Look at what uses memory under Processes on the Overview. Close something, add swap or memory, or lower this program's cache settings.")},
	{"exec_missing", 94, "critical", regexp.MustCompile(`(?i)failed to (?:execute|locate executable)|status=203/EXEC|exec format error|command not found|status=127\b`), func(l string) (string, string, string, bool) {
		p := path(l)
		if strings.Contains(strings.ToLower(l), "exec format error") {
			return "The program is built for another kind of machine", "Its file exists but cannot run here, usually because it was built for another processor (ARM against x86).",
				"Install the build that matches this machine (`uname -m` shows which).", true
		}
		return "Its program file is missing" + named(p), "systemd could not start it because the file its service runs does not exist or is not executable.",
			"Check the `ExecStart=` path with `systemctl cat <service>`, and reinstall the app if the file is gone.", true
	}},
	{"user_missing", 93, "critical", regexp.MustCompile(`(?i)status=217/USER|failed to determine user credentials|getpwnam.*(?:no such|not found)`), fixed("Its user account does not exist",
		"The service is set to run as a user that is not on this machine.",
		"Create the user, or fix `User=` in the service (`systemctl cat <service>` shows it).")},
	{"chdir", 92, "critical", regexp.MustCompile(`(?i)status=200/CHDIR|changing to the requested working directory failed`), fixed("Its working folder is missing",
		"systemd could not enter the folder the service is set to start in.",
		"Check `WorkingDirectory=` in the service, and that its drive is mounted.")},
	{"mount_gone", 91, "critical", regexp.MustCompile(`(?i)transport endpoint is not connected|stale file handle|is not mounted|not a mount ?point`), func(l string) (string, string, string, bool) {
		p := path(l)
		return "A mounted folder is gone" + named(p), "A network share or merged drive it uses has dropped out, so its files are not reachable.",
			"Remount it (`sudo mount -a`), or check the share or drive it comes from.", true
	}},
	{"config", 90, "critical", regexp.MustCompile(`(?i)(?:yaml|json|toml|xml|ini|config(?:uration)?(?: file)?)\b[^.]*\b(?:parse|parsing|syntax|unmarshal|invalid|unexpected|malformed)|invalid (?:config|configuration|setting)|failed to (?:load|parse|read) (?:the )?config|configuration error`), func(l string) (string, string, string, bool) {
		p := path(l)
		return "Its settings file has an error" + named(p), "It could not read its configuration, so it stopped before starting.",
			"Open the file and fix the spot the log line points to. A stray tab or a missing quote is the usual cause.", true
	}},
	{"py_module", 89, "critical", regexp.MustCompile(`(?:ModuleNotFoundError|ImportError): No module named '?([\w.]+)`), func(l string) (string, string, string, bool) {
		mod := first(moduleRe, l)
		return "Python module " + mod + " is missing", "A Python package it needs is not installed where it looks for it.",
			"Install it into the app's own environment (`pip install " + strings.Split(mod, ".")[0] + "` inside its venv), or reinstall the app.", true
	}},
	{"node_module", 89, "critical", regexp.MustCompile(`Cannot find module '([^']+)'`), func(l string) (string, string, string, bool) {
		return "Node module " + first(quoted, l) + " is missing", "A JavaScript package it needs is not installed.", "Run `npm install` in its folder, or reinstall the app.", true
	}},
	{"glibc", 88, "critical", regexp.MustCompile(`GLIBC_[\d.]+'? not found|version .GLIBC`), fixed("It needs a newer system library",
		"The program was built for a newer Linux than this one, so a system library is too old.",
		"Install a build made for this release, or update the system.")},
	{"permission", 87, "critical", regexp.MustCompile(`(?i)permission denied|EACCES|access to the path .* is denied|operation not permitted|EPERM|UnauthorizedAccessException`), func(l string) (string, string, string, bool) {
		p := path(l)
		if p == "" {
			return "It was refused permission", "It tried to read, write or open something its user account is not allowed to.",
				"Find the file or folder in the log line and give the service's user access to it.", true
		}
		return "It can't access " + p, "The user the service runs as is not allowed to read or write " + p + ".",
			"See who owns it with `ls -ld " + p + "`, then give the service's user access, for example `sudo chown -R <user>: " + p + "`.", true
	}},
	{"not_found", 86, "warning", regexp.MustCompile(`(?i)no such file or directory|ENOENT|FileNotFoundException|DirectoryNotFoundException|could not find (?:file|a part of the path)|cannot open shared object file`), func(l string) (string, string, string, bool) {
		p := path(l)
		if strings.Contains(l, "shared object") {
			return "A system library is missing", "The program needs a library that is not installed.", "Install the library named in the log line with your package manager.", true
		}
		return "A file it needs is missing" + named(p), "It looked for a file or folder that is not there.",
			"Check the path in its settings, and whether the drive that holds it is mounted.", true
	}},
	{"db_corrupt", 85, "critical", regexp.MustCompile(`(?i)database disk image is malformed|SQLITE_CORRUPT|file is not a database|database corruption`), fixed("Its database is damaged",
		"The database file is corrupt, often after a crash, a full disk or a power cut.",
		"Stop the app and restore its last backup (most *arr apps keep them under Backups). Check the drive's health too.")},
	{"db_locked", 84, "warning", regexp.MustCompile(`(?i)database is locked|SQLITE_BUSY`), fixed("Its database is locked",
		"Something else holds its database: a second copy of the app, a backup, or a database kept on a network share.",
		"Make sure only one copy runs, and keep its database on a local disk.")},
	{"segfault", 83, "critical", regexp.MustCompile(`(?i)segmentation fault|SIGSEGV|core dumped|signal 11\b|status=11/SEGV|status=139\b`), fixed("It crashed",
		"The program hit a bug or bad memory and was stopped by the system.",
		"Update it to the latest version. If it keeps crashing, check memory with a memtest and the app's issue tracker.")},
	{"too_many_files", 82, "warning", regexp.MustCompile(`(?i)too many open files|EMFILE`), fixed("It hit the open-file limit",
		"It has more files and connections open than it is allowed.",
		"Raise the limit with `LimitNOFILE=65536` in its service, then restart it.")},
	{"tls", 70, "warning", regexp.MustCompile(`(?i)x509:|certificate (?:has )?expired|certificate verify failed|unknown authority|self[- ]signed certificate|CERT_HAS_EXPIRED|SSL routines|tls: (?:bad|handshake)`), func(l string) (string, string, string, bool) {
		low := strings.ToLower(l)
		switch {
		case strings.Contains(low, "expired"):
			return "A certificate has expired", "A secure connection failed because the server's certificate is out of date.", "Renew the certificate on that server.", true
		case strings.Contains(low, "unknown authority") || strings.Contains(low, "self-signed") || strings.Contains(low, "self signed"):
			return "A certificate is not trusted", "The server's certificate is self-signed or from an authority this machine does not trust.", "Use a trusted certificate, or tell this app to accept that one.", true
		}
		return "A secure connection failed", "The TLS handshake with a server did not succeed.", "Check the server's certificate and the address used.", true
	}},
	{"auth", 68, "warning", regexp.MustCompile(`(?i)authentication failed|invalid (?:api[ _-]?key|credentials|token|password)|401 unauthorized|unauthorized \(401\)|password authentication failed|access denied for user|login failed`), fixed("A login was refused",
		"It connected to something but its password, token or API key was not accepted.",
		"Update the credentials in its settings; they may have changed on the other side.")},
	{"dns", 66, "warning", regexp.MustCompile(`(?i)no such host|name or service not known|temporary failure in name resolution|EAI_AGAIN|ENOTFOUND|could not resolve host|NXDOMAIN|name resolution failed`), func(l string) (string, string, string, bool) {
		h := first(hostRe, l)
		return "It can't look up " + or(h, "a host name"), "The name it tries to reach does not resolve: a typo, or DNS is not answering.",
			"Check the address in its settings, and that this machine has working DNS (`resolvectl query " + or(h, "example.com") + "`).", true
	}},
	{"unreachable", 64, "warning", regexp.MustCompile(`(?i)network is unreachable|ENETUNREACH|no route to host|EHOSTUNREACH`), fixed("The network can't reach it",
		"There is no route to the address it tries to reach: the network is down or the address is wrong.",
		"Check this machine's network connection and the address in its settings.")},
	{"refused", 62, "warning", regexp.MustCompile(`(?i)connection refused|ECONNREFUSED`), func(l string) (string, string, string, bool) {
		var a string
		for _, m := range addrRe.FindAllStringSubmatch(l, -1) {
			if strings.Contains(m[1], ":") || strings.Count(m[1], ".") == 3 || m[1] == "localhost" {
				a = m[1]
			}
		}
		return "Nothing answers at " + or(a, "the address it uses"), "It tried to connect, but nothing is listening there: that program is down, or on another port.",
			"Start the program it depends on, or correct the address and port in its settings.", true
	}},
	{"http_5xx", 50, "warning", regexp.MustCompile(`\bHTTP[/ ]?(?:\d\.\d )?5\d\d\b|\b(?:502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout)\b`), func(l string) (string, string, string, bool) {
		code := first(httpRe, l)
		switch code {
		case "502", "504":
			return "The proxy can't reach the app (HTTP " + code + ")", "A reverse proxy answered, but the app behind it is down or too slow.", "Start the app behind the proxy, or check the address the proxy forwards to.", true
		case "503":
			return "The app is unavailable (HTTP 503)", "The server is up but not ready to serve: starting, overloaded, or in maintenance.", "Give it a moment; if it stays, look at the app's own log.", true
		}
		return "The server hit an error (HTTP " + or(code, "5xx") + ")", "The server answered with an internal error.", "The app's own log shows what failed.", true
	}},
	{"http_404", 45, "warning", regexp.MustCompile(`\bHTTP[/ ]?(?:\d\.\d )?404\b|\b404 Not Found\b`), fixed("The address is not found (HTTP 404)",
		"The server answered, but nothing lives at that path.",
		"Check the URL: a changed base path or a missing slash is common.")},
	{"timeout", 40, "warning", regexp.MustCompile(`(?i)\btimed? ?out\b|ETIMEDOUT|deadline exceeded|operation timed out`), fixed("Something it talks to is not answering",
		"A connection or request took too long and was given up.",
		"Check that the other end is up and reachable, and not overloaded.")},
	{"start_timeout", 30, "warning", regexp.MustCompile(`(?i)start(?:-post|-pre)? operation timed out|failed with result 'timeout'`), fixed("It took too long to start",
		"systemd waited for it to report ready, then gave up.",
		"Look at the lines before this one for what it was waiting on. A slow disk or a missing network share is common.")},
	{"dependency", 25, "warning", regexp.MustCompile(`(?i)dependency failed for|dependency job for .* failed`), fixed("Something it depends on failed first",
		"systemd did not start it because a service or mount it needs had failed.",
		"Fix the service or mount named in the log; this one starts once that does.")},
	{"exception", 20, "warning", regexp.MustCompile(`(?:^|\s)([A-Z]\w*(?:Error|Exception)): (.+)|^panic: (.+)|Unhandled exception`), func(l string) (string, string, string, bool) {
		m := exceptionRe.FindStringSubmatch(l)
		if m == nil {
			return "It crashed with an error", "The program hit an error it did not handle and stopped.", "The log line shows the error. Updating the app often fixes it.", true
		}
		msg := m[2]
		if len(msg) > 90 {
			msg = msg[:90] + "…"
		}
		return "It crashed: " + m[1], msg, "Search the app's issue tracker for this error, or update the app.", true
	}},
	{"start_limit", 10, "warning", regexp.MustCompile(`(?i)start request repeated too quickly|start-limit-hit`), fixed("systemd stopped retrying",
		"It failed several times in a row, so systemd stopped restarting it.",
		"Fix the cause above, then start it again (or `sudo systemctl reset-failed <service>`).")},
}

func match(msg string) (Finding, bool) {
	for _, r := range rules {
		if !r.re.MatchString(msg) {
			continue
		}
		title, explain, fix, ok := r.build(msg)
		if !ok {
			continue
		}
		return Finding{ID: r.id, Title: title, Explain: explain, Fix: fix, Severity: r.severity, Line: clip(msg), Known: true, rank: 100 + r.rank, key: r.id + "|" + title}, true
	}
	return Finding{}, false
}

// exit explains systemd's own exit codes and results when the log lines do not.
func exit(c Context) (Finding, bool) {
	codes := map[int]string{200: "chdir", 203: "exec_missing", 217: "user_missing", 137: "oom", 139: "segfault"}
	results := map[string]string{"oom-kill": "oom", "core-dump": "segfault", "start-limit-hit": "start_limit", "timeout": "start_timeout"}
	id := cmp.Or(results[c.Result], codes[c.ExitStatus])
	if id == "" {
		return Finding{}, false
	}
	for _, r := range rules {
		if r.id == id {
			title, explain, fix, _ := r.build("")
			return Finding{ID: id, Title: title, Explain: explain, Fix: fix, Severity: r.severity, Known: true, rank: 100 + r.rank - 1, key: id + "|" + title}, true
		}
	}
	return Finding{}, false
}

// ---------- grouping everything else ----------

var (
	errorWords = regexp.MustCompile(`(?i)\b(?:error|err|fatal|panic|critical|crit|exception|failed|failure|cannot|can't|unable to|denied|refused|traceback)\b|\[(?:error|fatal|crit)\]|\blevel=(?:error|fatal)`)
	masks      = []struct {
		re   *regexp.Regexp
		with string
	}{
		{regexp.MustCompile(`"[^"]*"|'[^']*'`), "‹…›"},
		{regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "‹id›"},
		{regexp.MustCompile(`(?:/[\w.@+~-]+)+/?`), "‹path›"},
		{regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b`), "‹addr›"},
		{regexp.MustCompile(`\b(?:0x)?[0-9a-f]{6,}\b`), "‹hex›"},
		{regexp.MustCompile(`\d+(?:[.,:]\d+)*`), "‹n›"},
		{regexp.MustCompile(`\s+`), " "},
	}
	levelPrefix = regexp.MustCompile(`(?i)^(?:\[?\d{4}-\d\d-\d\d[ T][\d:.,]+\]?\s*)?(?:\[?(?:error|err|fatal|crit|critical|warn|warning)\]?[:|\s]+)?`)
)

func looksLikeError(msg string) bool { return errorWords.MatchString(msg) }

// shape is the message with everything that changes from one occurrence to the next masked out.
func shape(msg string) string {
	s := strings.ToLower(levelPrefix.ReplaceAllString(msg, ""))
	for _, m := range masks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	return strings.TrimSpace(s)
}

func group(msg string) Finding {
	title := levelPrefix.ReplaceAllString(msg, "")
	if len(title) > 110 {
		title = title[:110] + "…"
	}
	return Finding{ID: "repeated", Title: title, Explain: "An error the logs repeat; the line below is the latest.", Severity: "warning", Line: clip(msg), rank: 0, key: "shape|" + shape(msg)}
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return strconv.Itoa(n) + " times"
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
