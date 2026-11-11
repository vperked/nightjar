package services_test

import (
	"bufio"
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"nightjar/internal/services"
)

// serve answers each connection on a local port with reply(conn), and returns host:port.
func serve(t *testing.T, reply func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); reply(c) }()
		}
	}()
	return ln.Addr().String()
}

func parsed(t *testing.T, js string) services.Spec {
	t.Helper()
	s, err := services.ParseSpec([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pgError(code, msg string) []byte {
	body := []byte("SFATAL\x00C" + code + "\x00M" + msg + "\x00\x00")
	return append(binary.BigEndian.AppendUint32([]byte{'E'}, uint32(4+len(body))), body...)
}

func TestPostgresCheck(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
		up    bool
		want  string
	}{
		{"asks for a password", []byte{'R', 0, 0, 0, 12, 0, 0, 0, 10, 0, 0, 0, 0}, true, "accepting"},
		{"unknown role", pgError("28000", `role "nightjar" does not exist`), true, "accepting"},
		{"starting up", pgError("57P03", "the database system is starting up"), false, "not accepting connections: the database system is starting up"},
		{"too many", pgError("53300", "sorry, too many clients already"), false, "out of connections"},
		{"not postgres", []byte("HTTP/1.1 400 Bad Request\r\n\r\n"), false, "not a PostgreSQL server"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr := serve(t, func(conn net.Conn) {
				var head [8]byte
				if _, err := conn.Read(head[:]); err != nil {
					return
				}
				conn.Write(c.reply)
			})
			r := services.Run(parsed(t, `{"name":"db","type":"postgres","target":"`+addr+`","timeout":2}`))
			if r.Up != c.up || !strings.Contains(r.Detail, c.want) {
				t.Errorf("got up=%v %q, want up=%v containing %q", r.Up, r.Detail, c.up, c.want)
			}
		})
	}
}

func mysqlPacket(body []byte) []byte {
	n := len(body)
	return append([]byte{byte(n), byte(n >> 8), byte(n >> 16), 0}, body...)
}

func TestMySQLCheck(t *testing.T) {
	greeting := mysqlPacket(append([]byte{10}, []byte("11.4.3-MariaDB\x00rest-of-handshake")...))
	tooMany := mysqlPacket(append([]byte{0xff, 0x10, 0x04}, []byte("Too many connections")...))              // 1040
	notAllowed := mysqlPacket(append([]byte{0xff, 0x6a, 0x04}, []byte("Host '10.0.0.9' is not allowed")...)) // 1130
	for _, c := range []struct {
		name  string
		reply []byte
		up    bool
		want  string
	}{
		{"greeting", greeting, true, "11.4.3-MariaDB"},
		{"MariaDB's compatibility prefix", mysqlPacket(append([]byte{10}, []byte("5.5.5-10.11.14-MariaDB-0ubuntu0.24.04.1\x00x")...)), true, "(10.11.14-MariaDB-0ubuntu0.24.04.1)"},
		{"too many connections", tooMany, false, "refused (1040): Too many connections"},
		{"host not allowed", notAllowed, true, "accepting"},
		{"silent", nil, false, "closed the connection"},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := serve(t, func(conn net.Conn) { conn.Write(c.reply) })
			r := services.Run(parsed(t, `{"name":"db","type":"mysql","target":"`+addr+`","timeout":2}`))
			if r.Up != c.up || !strings.Contains(r.Detail, c.want) {
				t.Errorf("got up=%v %q, want up=%v containing %q", r.Up, r.Detail, c.up, c.want)
			}
		})
	}
}

func TestRedisCheck(t *testing.T) {
	for _, c := range []struct {
		reply string
		up    bool
		want  string
	}{
		{"+PONG\r\n", true, "PONG"},
		{"-NOAUTH Authentication required.\r\n", true, "needs a password"},
		{"-LOADING Redis is loading the dataset in memory\r\n", false, "LOADING"},
		{"SSH-2.0-OpenSSH_9.6\r\n", false, "not a Redis server"},
	} {
		t.Run(c.reply, func(t *testing.T) {
			addr := serve(t, func(conn net.Conn) {
				if line, _ := bufio.NewReader(conn).ReadString('\n'); line == "PING\r\n" {
					conn.Write([]byte(c.reply))
				}
			})
			r := services.Run(parsed(t, `{"name":"cache","type":"redis","target":"`+addr+`","timeout":2}`))
			if r.Up != c.up || !strings.Contains(r.Detail, c.want) {
				t.Errorf("got up=%v %q, want up=%v containing %q", r.Up, r.Detail, c.up, c.want)
			}
		})
	}
}

func TestDNSCheckLocalhost(t *testing.T) {
	r := services.Run(parsed(t, `{"name":"lo","type":"dns","target":"localhost","timeout":3}`))
	if !r.Up || !strings.Contains(r.Detail, "127.0.0.1") {
		t.Fatalf("got %+v, want localhost to resolve to 127.0.0.1", r)
	}
	r = services.Run(parsed(t, `{"name":"lo","type":"dns","target":"localhost","keyword":"10.9.9.9","timeout":3}`))
	if r.Up || !strings.Contains(r.Detail, "does not include") {
		t.Fatalf("got %+v, want a miss on the expected answer", r)
	}
}

func TestNewTypesValidate(t *testing.T) {
	good := []string{
		`{"name":"a","type":"dns","target":"_dmarc.example.com.","record_type":"txt","dns_server":"1.1.1.1"}`,
		`{"name":"a","type":"dns","target":"example.com","dns_server":"[2606:4700::1111]:53"}`,
		`{"name":"a","type":"postgres","target":"db.lan"}`,
		`{"name":"a","type":"mysql","target":"10.0.0.5:3307"}`,
		`{"name":"a","type":"redis","target":"[::1]:6379"}`,
	}
	for _, js := range good {
		if _, err := services.ParseSpec([]byte(js)); err != nil {
			t.Errorf("%s: %v", js, err)
		}
	}
	s := parsed(t, good[0])
	if s.RecordType != "TXT" || s.Target != "_dmarc.example.com" {
		t.Errorf("record type %q, target %q: want TXT and no trailing dot", s.RecordType, s.Target)
	}
	if parsed(t, `{"name":"a","type":"dns","target":"example.com"}`).RecordType != "A" {
		t.Error("record type should default to A")
	}
	bad := []string{
		`{"name":"a","type":"dns","target":"exa mple.com"}`,
		`{"name":"a","type":"dns","target":"example.com","record_type":"SRV"}`,
		`{"name":"a","type":"dns","target":"example.com","dns_server":"1.1.1.1:99999"}`,
		`{"name":"a","type":"postgres","target":"db.lan:notaport"}`,
		`{"name":"a","type":"redis","target":"bad host"}`,
	}
	for _, js := range bad {
		if _, err := services.ParseSpec([]byte(js)); err == nil {
			t.Errorf("%s: accepted", js)
		}
	}
}

// With a password, AUTH goes first as a RESP array; a refused password is down, and so is LOADING,
// which a server with a password hides from a PING sent without one.
func TestRedisCheckWithPassword(t *testing.T) {
	for _, c := range []struct {
		name, authReply, pingReply string
		up                         bool
		want                       string
	}{
		{"right password", "+OK\r\n", "+PONG\r\n", true, "PONG"},
		{"wrong password", "-WRONGPASS invalid username-password pair or user is disabled.\r\n", "", false, "password was refused"},
		{"old wrong password", "-ERR invalid password\r\n", "", false, "password was refused"},
		{"loading", "+OK\r\n", "-LOADING Redis is loading the dataset in memory\r\n", false, "LOADING"},
		{"no password set", "-ERR AUTH <password> called without any password configured for the default user.\r\n", "+PONG\r\n", true, "no password set"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := make(chan string, 1)
			addr := serve(t, func(conn net.Conn) {
				r := bufio.NewReader(conn)
				var lines []string
				for i := 0; i < 5; i++ { // *2, $4, AUTH, $n, password
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					lines = append(lines, l)
				}
				got <- strings.Join(lines, "")
				conn.Write([]byte(c.authReply))
				if c.pingReply != "" {
					if l, _ := r.ReadString('\n'); l == "PING\r\n" {
						conn.Write([]byte(c.pingReply))
					}
				}
			})
			r := services.Run(parsed(t, `{"name":"cache","type":"redis","target":"`+addr+`","password":"pa ss\"word","timeout":2}`))
			if r.Up != c.up || !strings.Contains(r.Detail, c.want) {
				t.Errorf("got up=%v %q, want up=%v containing %q", r.Up, r.Detail, c.up, c.want)
			}
			if sent := <-got; sent != "*2\r\n$4\r\nAUTH\r\n$10\r\npa ss\"word\r\n" {
				t.Errorf("sent %q", sent)
			}
		})
	}
}
