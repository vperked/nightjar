package remote

// A server can also be just an SSH login, for a machine without the agent: Nightjar shows whether its
// SSH port answers, lists the checks that belong to it, and can log in to test or reboot it. It has
// no readings and raises no alerts of its own; its checks do that.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// KindSSH is a server watched through nothing but its SSH login.
const KindSSH = "ssh"

func (h Host) isSSHOnly() bool { return h.Kind == KindSSH }

// AddSSHServer watches a server through its SSH login alone. Its SSH port must answer first.
func (m *Monitor) AddSSHServer(ctx context.Context, name string, login SSH) (State, error) {
	c, err := m.NormalizeSSH(ctx, login)
	if err != nil {
		return State{}, err
	}
	if c.Host == "" {
		return State{}, errors.New("enter the server's address")
	}
	name = cmp.Or(strings.TrimSpace(name), c.Host)
	if err := validName(name); err != nil {
		return State{}, err
	}
	m.mu.Lock()
	for _, e := range m.hosts {
		if e.host.isSSHOnly() && e.host.Address == c.Host && e.host.SSH.Port == c.Port {
			m.mu.Unlock()
			return State{}, ErrExists
		}
	}
	m.mu.Unlock()
	if err := m.dialSSH(ctx, c); err != nil {
		return State{}, err
	}
	h := Host{ID: newID(), Name: name, Kind: KindSSH, Address: c.Host, SSH: &c, Added: float64(m.opt.Now().Unix())}
	e := newEntry(h)
	e.status, e.lastSeen = "online", m.opt.Now()
	m.mu.Lock()
	m.hosts = append(m.hosts, e)
	err = m.saveLocked()
	st := e.state(History)
	m.mu.Unlock()
	if err != nil {
		return State{}, err
	}
	log.Info("Server added", "name", name, "ssh", c.User+"@"+c.Host)
	return st, nil
}

// Rename gives an SSH-only server a new name; its address and login change under SSH.
func (m *Monitor) Rename(id, name string) (State, error) {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		return State{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.find(id)
	if e == nil {
		return State{}, ErrNotFound
	}
	e.host.Name = name
	if err := m.saveLocked(); err != nil {
		return State{}, err
	}
	return e.state(History), nil
}

// dialSSH says whether something accepts connections on a login's SSH port.
func (m *Monitor) dialSSH(ctx context.Context, c SSH) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		return fmt.Errorf("nothing answered SSH at %s: %s", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), plainError(err))
	}
	conn.Close()
	return nil
}

// pollSSH takes in one try of an SSH-only server's port.
func (m *Monitor) pollSSH(ctx context.Context, e *entry, h Host) {
	err := m.dialSSH(ctx, *h.SSH)
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.find(h.ID) != e {
		return
	}
	if err != nil {
		e.fails++
		e.err = err.Error()
		if e.fails >= offlineAfter {
			e.status = "offline"
		}
		return
	}
	e.fails, e.err, e.status, e.lastSeen = 0, "", "online", m.opt.Now()
}
