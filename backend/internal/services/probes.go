package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DNS, PostgreSQL, MySQL/MariaDB and Redis checks. The database checks speak just enough of each
// wire protocol to tell a server that is accepting connections from one that is up but refusing
// them (starting up, out of connections, loading its data), without logging in, so they need no
// credentials and no client libraries.

// RecordTypes are the DNS record types a dns check can ask for.
var RecordTypes = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}

// dnsNameRE is a domain name; underscores are allowed for names such as _dmarc.example.com.
var dnsNameRE = regexp.MustCompile(`^[A-Za-z0-9_]([A-Za-z0-9_.\-]*[A-Za-z0-9_.])?$`)

// Default ports for the database checks, used when the target has none.
const (
	postgresPort = 5432
	mysqlPort    = 3306
	redisPort    = 6379
)

// CheckDNS resolves the target's records of RecordType (A by default), through DNSServer when it is
// set and the system resolver otherwise. It is up when there is at least one answer and, when
// Keyword is set, one of the answers contains it (ignoring case).
func CheckDNS(s Spec) Result {
	name := strings.TrimSuffix(strings.TrimSpace(s.Target), ".")
	kind := strings.ToUpper(s.RecordType)
	if kind == "" {
		kind = "A"
	}
	r := net.DefaultResolver
	via := ""
	if s.DNSServer != "" {
		host, port, err := splitHostPort(s.DNSServer, 53)
		if err != nil {
			return down(err.Error())
		}
		addr := net.JoinHostPort(host, port)
		via = " from " + addr
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
	defer cancel()
	start := time.Now()
	var answers []string
	var err error
	switch kind {
	case "A", "AAAA":
		network := map[string]string{"A": "ip4", "AAAA": "ip6"}[kind]
		var ips []net.IP
		ips, err = r.LookupIP(ctx, network, name)
		for _, ip := range ips {
			answers = append(answers, ip.String())
		}
	case "CNAME":
		var c string
		c, err = r.LookupCNAME(ctx, name)
		// A name with no CNAME resolves to itself.
		if c = strings.TrimSuffix(c, "."); err == nil && !strings.EqualFold(c, name) {
			answers = append(answers, c)
		}
	case "MX":
		var mx []*net.MX
		mx, err = r.LookupMX(ctx, name)
		for _, m := range mx {
			answers = append(answers, fmt.Sprintf("%d %s", m.Pref, strings.TrimSuffix(m.Host, ".")))
		}
	case "TXT":
		answers, err = r.LookupTXT(ctx, name)
	case "NS":
		var ns []*net.NS
		ns, err = r.LookupNS(ctx, name)
		for _, n := range ns {
			answers = append(answers, strings.TrimSuffix(n.Host, "."))
		}
	default:
		return down("unknown record type " + kind)
	}
	if err != nil {
		return downAfter(start, describeDNSErr(err, s.Timeout))
	}
	if len(answers) == 0 {
		return downAfter(start, fmt.Sprintf("no %s records%s", kind, via))
	}
	if s.Keyword != "" {
		want := strings.ToLower(s.Keyword)
		if !slices.ContainsFunc(answers, func(a string) bool { return strings.Contains(strings.ToLower(a), want) }) {
			return downAfter(start, fmt.Sprintf("%s answer %s does not include %q", kind, summarizeAnswers(answers), s.Keyword))
		}
	}
	return up(start, kind+" "+summarizeAnswers(answers)+via)
}

func summarizeAnswers(a []string) string {
	const show = 3
	if len(a) <= show {
		return strings.Join(a, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(a[:show], ", "), len(a)-show)
}

func describeDNSErr(err error, timeout float64) string {
	var de *net.DNSError
	if errors.As(err, &de) {
		switch {
		case de.IsTimeout:
			return fmt.Sprintf("no answer within %gs", timeout)
		case de.IsNotFound:
			return "no such name (NXDOMAIN)"
		}
		return "lookup failed: " + de.Err
	}
	return "lookup failed: " + err.Error()
}

// dial opens the TCP connection a database check talks over, with the check's deadline on it.
func dial(s Spec, defaultPort int) (net.Conn, time.Time, *Result) {
	host, port, err := splitHostPort(s.Target, defaultPort)
	if err != nil {
		r := down(err.Error())
		return nil, time.Time{}, &r
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), s.timeout())
	if err != nil {
		r := down(describeNetErr(err, s.Timeout))
		return nil, start, &r
	}
	conn.SetDeadline(start.Add(s.timeout()))
	return conn, start, nil
}

func describeReadErr(err error, timeout float64, what string) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("connected, but no %s within %gs", what, timeout)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "connected, but the server closed the connection"
	}
	return "connected, but " + err.Error()
}

// postgresBusy are the SQLSTATEs of a server that is up but not taking connections.
var postgresBusy = map[string]string{
	"57P03": "not accepting connections",
	"53300": "out of connections",
	"57P01": "shutting down",
	"53100": "out of disk space",
	"53200": "out of memory",
}

// CheckPostgres sends a startup message, as pg_isready does. Any request to authenticate, or a login
// refused for who we are, means the server is accepting connections; starting up, shutting down,
// recovering or being out of connections means it is not.
func CheckPostgres(s Spec) Result {
	conn, start, failed := dial(s, postgresPort)
	if failed != nil {
		return *failed
	}
	defer conn.Close()
	var params bytes.Buffer
	for _, kv := range [][2]string{{"user", "nightjar"}, {"database", "postgres"}, {"application_name", "nightjar-check"}} {
		params.WriteString(kv[0] + "\x00" + kv[1] + "\x00")
	}
	params.WriteByte(0)
	msg := binary.BigEndian.AppendUint32(nil, uint32(8+params.Len()))
	msg = binary.BigEndian.AppendUint32(msg, 196608) // protocol 3.0
	msg = append(msg, params.Bytes()...)
	if _, err := conn.Write(msg); err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
	}
	var head [5]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
	}
	size := int(binary.BigEndian.Uint32(head[1:])) - 4
	switch head[0] {
	case 'R':
		conn.Write([]byte{'X', 0, 0, 0, 4}) // Terminate
		return up(start, "accepting connections")
	case 'E':
		if size < 0 || size > 8192 {
			return downAfter(start, "not a PostgreSQL server")
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(conn, body); err != nil {
			return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
		}
		code, message := postgresError(body)
		if why, busy := postgresBusy[code]; busy {
			return downAfter(start, fmt.Sprintf("%s: %s", why, message))
		}
		// Refused for our user, database or address: the server itself is taking connections.
		return up(start, "accepting connections")
	}
	return downAfter(start, "not a PostgreSQL server")
}

// postgresError reads the SQLSTATE and message out of an ErrorResponse body.
func postgresError(body []byte) (code, message string) {
	for len(body) > 1 && body[0] != 0 {
		field := body[0]
		end := bytes.IndexByte(body[1:], 0)
		if end < 0 {
			break
		}
		value := string(body[1 : 1+end])
		switch field {
		case 'C':
			code = value
		case 'M':
			message = value
		}
		body = body[2+end:]
	}
	return code, message
}

// mysqlUp are MySQL errors sent in place of the greeting that still mean the server is running:
// it refuses this address (1130), or is waiting to be set up.
var mysqlUp = map[uint16]bool{1130: true}

// CheckMySQL reads the greeting a MySQL or MariaDB server sends on connect, which names its version.
// An error in its place (too many connections, host blocked) means it is not taking connections.
func CheckMySQL(s Spec) Result {
	conn, start, failed := dial(s, mysqlPort)
	if failed != nil {
		return *failed
	}
	defer conn.Close()
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "greeting"))
	}
	size := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	if size < 1 || size > 64<<10 {
		return downAfter(start, "not a MySQL server")
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(conn, body); err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "greeting"))
	}
	switch body[0] {
	case 10: // protocol 10: the version, NUL-terminated, comes next
		version := body[1:]
		if i := bytes.IndexByte(version, 0); i >= 0 {
			version = version[:i]
		}
		// MariaDB puts "5.5.5-" before its real version, for old clients.
		v := string(version)
		if strings.Contains(v, "MariaDB") {
			v = strings.TrimPrefix(v, "5.5.5-")
		}
		return up(start, "accepting connections ("+v+")")
	case 0xff:
		if len(body) < 3 {
			return downAfter(start, "refused")
		}
		code := binary.LittleEndian.Uint16(body[1:3])
		message := body[3:]
		if len(message) >= 6 && message[0] == '#' {
			message = message[6:] // the SQLSTATE marker
		}
		if mysqlUp[code] {
			return up(start, "accepting connections")
		}
		return downAfter(start, fmt.Sprintf("refused (%d): %s", code, strings.TrimSpace(string(message))))
	}
	return downAfter(start, "not a MySQL server")
}

// CheckRedis sends PING, after AUTH when the check has a password. PONG is up. Without a password,
// being asked for one is up too, since the server is answering, though it hides LOADING behind
// that; with one, a refused password is down. LOADING, MASTERDOWN and other errors are down.
func CheckRedis(s Spec) Result {
	conn, start, failed := dial(s, redisPort)
	if failed != nil {
		return *failed
	}
	defer conn.Close()
	in := bufio.NewReaderSize(io.LimitReader(conn, 4096), 1024)
	reply := func() (string, error) {
		line, err := in.ReadString('\n')
		return strings.TrimSpace(line), err
	}
	noPassword := false
	if s.Password != "" {
		// AUTH and PING as one RESP array each, so a password with spaces or quotes goes as it is.
		auth := fmt.Sprintf("*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(s.Password), s.Password)
		if _, err := conn.Write([]byte(auth)); err != nil {
			return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
		}
		line, err := reply()
		if err != nil {
			return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
		}
		switch {
		case line == "+OK":
		case strings.Contains(line, "without any password configured"):
			noPassword = true // it answers, and needs no password: the check still means something
		case strings.HasPrefix(line, "-WRONGPASS"), strings.HasPrefix(line, "-ERR invalid password"), strings.HasPrefix(line, "-ERR invalid username-password"):
			return downAfter(start, "the password was refused")
		case strings.HasPrefix(line, "-"):
			return downAfter(start, strings.TrimPrefix(line, "-"))
		default:
			return downAfter(start, "not a Redis server: "+strconv.Quote(truncate(line, 60)))
		}
	}
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
	}
	line, err := reply()
	if err != nil {
		return downAfter(start, describeReadErr(err, s.Timeout, "answer"))
	}
	switch {
	case line == "+PONG" && noPassword:
		return up(start, "PONG (it has no password set)")
	case line == "+PONG":
		return up(start, "PONG")
	case strings.HasPrefix(line, "-NOAUTH"), strings.HasPrefix(line, "-WRONGPASS"):
		return up(start, "answering (needs a password)")
	case strings.HasPrefix(line, "-"):
		return downAfter(start, strings.TrimPrefix(line, "-"))
	}
	return downAfter(start, "not a Redis server: "+strconv.Quote(truncate(line, 60)))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
