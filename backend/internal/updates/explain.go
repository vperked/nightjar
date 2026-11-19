package updates

import (
	"path/filepath"
	"regexp"
)

// explanations turn what a failing command printed into a sentence for the dashboard. The raw
// output still reaches the update's log and Nightjar's own log. The first match wins, so the
// specific come before the general (a download that failed because of DNS says DNS). Those marked
// pkg speak of package sources and apt, so they are only for the package managers: the same words
// from a target's own command (a curl that got a 404, a git remote that would not resolve) mean
// something else, and what it printed says it better.
var explanations = []struct {
	re  *regexp.Regexp
	why string
	pkg bool
}{
	{regexp.MustCompile(`No space left on device|You don't have enough free space`), "the disk is full. Free some space and try again", false},
	{regexp.MustCompile(`sudo: a password is required|sudo: a terminal is required`), "Nightjar needs passwordless sudo for this", false},
	{regexp.MustCompile(`dpkg was interrupted`), "an earlier install was interrupted and could not be finished. Run dpkg --configure -a on the server", true},
	{regexp.MustCompile(`Temporary failure resolving|Could not resolve|Name or service not known`), "the package servers' addresses could not be looked up. Check the server's DNS and internet connection", true},
	{regexp.MustCompile(`does not have a Release file|no longer has a Release file|404\s+Not Found`), "a package source is no longer at its address, often because that release reached end of life. Fix or remove it in /etc/apt/sources.list.d", true},
	{regexp.MustCompile(`NO_PUBKEY|EXPKEYSIG|is not signed|signatures couldn't be verified|signatures were invalid`), "a package source's signing key is missing or expired, so apt will not trust it", true},
	{regexp.MustCompile(`is not valid yet`), "the server's clock is behind, so apt rejects the package lists as not yet valid", true},
	{regexp.MustCompile(`Failed to fetch|Unable to fetch|Could not connect to|Connection timed out|Network is unreachable|Connection refused`), "the package servers could not be reached. Check the server's internet connection", true},
	{regexp.MustCompile(`Unmet dependencies|held broken packages|unmet dependencies`), "some packages need versions of others that apt cannot install on its own", true},
}

// packageManagerNames are the programs whose failures the pkg explanations are about.
var packageManagerNames = map[string]bool{"apt-get": true, "apt": true, "apt-cache": true, "dpkg": true, "dnf": true, "yum": true, "pacman": true, "snap": true}

func isPackageCommand(argv []string) bool {
	return len(argv) > 0 && packageManagerNames[filepath.Base(argv[0])]
}

// explain says in words why the command argv failed; empty when its output is not one Nightjar knows.
func explain(argv []string, out Output) string {
	text := out.Stderr + "\n" + out.Stdout
	pkg := isPackageCommand(argv)
	if l, ok := findAptLock(out); ok && pkg {
		return l.who() + " was using the package manager and did not let go. Try again when it has finished"
	}
	for _, e := range explanations {
		if (pkg || !e.pkg) && e.re.MatchString(text) {
			return e.why
		}
	}
	return ""
}
