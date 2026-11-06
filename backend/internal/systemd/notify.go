package systemd

import (
	"errors"
	"net"
	"os"
)

// Notify sends state (such as "EXTEND_TIMEOUT_USEC=60000000") to the service manager that started
// this process, as sd_notify does. It is an error when there is none to tell: not run by systemd, or
// a unit without NotifyAccess.
func Notify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return errors.New("no NOTIFY_SOCKET")
	}
	if addr[0] == '@' { // an abstract socket
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
