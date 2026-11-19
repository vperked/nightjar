package firewall

// Locks pin a port to the service listening on it. While another program holds a locked port (the
// service stopped and something else took the port, or something else listens beside it) the firewall
// drops what comes to that port from the network, ahead of every rule, and says so; when the port is
// the service's again, or nothing holds it, the drop is lifted. A port nothing listens on is left be:
// the machine refuses what comes to it anyway, and a service restarting is not cut off.
//
// The service is its systemd unit when it runs in one: whatever runs in that unit may hold the port,
// so an upgrade that moves its executable, or helpers it hands its socket to (Postfix's smtpd, Dovecot's
// imap-login), are still the service. Outside a unit it is its executable, and the processes it starts.
// A program listening on loopback alone takes nothing, since the network cannot reach it.
//
// The firewall cannot tell which program a packet will reach, so the port is watched instead, every
// few seconds. The ports kept open whatever the rules (SSH's, the dashboard's, the agent's) are only
// watched: a mistake there would cut everyone off, the dashboard included.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Lock pins a port to a service: the one listening on it when it was locked.
type Lock struct {
	ID    string `json:"id"`
	Port  uint16 `json:"port"`
	Proto string `json:"proto"`
	// Service is what may hold the port: its systemd service when it had one, else its executable.
	Service
	Added float64 `json:"added"`
}

// The states of a lock.
const (
	// LockHeld is a locked port held by its service.
	LockHeld = "held"
	// LockIdle is a locked port nothing listens on.
	LockIdle = "idle"
	// LockTaken is a locked port another program holds: what comes to it is dropped, unless it is
	// one kept open.
	LockTaken = "taken"
)

// LockState is a lock and how its port stands.
type LockState struct {
	Lock
	State string `json:"state"`
	// Holder is what holds the port instead, while it is taken.
	Holder *Service `json:"holder,omitempty"`
	// Since is when it was taken.
	Since float64 `json:"since,omitempty"`
	// WatchOnly is a lock on a port kept open whatever the rules (SSH's, the dashboard's, the
	// agent's), whose traffic is never dropped.
	WatchOnly bool `json:"watch_only,omitempty"`
}

// Words puts how a lock stands in words, for alerts: "port 5432/tcp is held by python3, not postgresql".
func (s LockState) Words() string {
	port := fmt.Sprintf("port %d/%s", s.Port, s.Proto)
	switch s.State {
	case LockTaken:
		by := "another program"
		if h := s.Holder; h != nil {
			// The program there, as it runs, and the unit it runs in when that is not the service's.
			by = cmp.Or(h.Program, filepath.Base(h.Exe))
			if h.Exe != "" && h.Exe != by {
				by += " (" + h.Exe + ")"
			}
			if h.Unit != "" && h.Unit != s.Unit {
				by += " in " + h.Unit
			}
		}
		return fmt.Sprintf("%s is held by %s, not %s", port, by, s.Name())
	case LockIdle:
		return fmt.Sprintf("nothing listens on %s; it is locked to %s", port, s.Name())
	}
	return fmt.Sprintf("%s is held by %s", port, s.Name())
}

// maxLocks is how many ports can be locked.
const maxLocks = 100

// fullScanEvery is how many lock checks go by before the holders are read again however the sockets
// look: a socket handed to another program (a service that runs a shell over itself) is the same
// socket.
const fullScanEvery = 6

// lockWatch is what the lock checks remember between them.
type lockWatch struct {
	sig   string // the locked ports' sockets at the last full read
	count int
}

// matches says whether a lock lets a process hold its port: anything in the service's systemd unit,
// else the same executable.
func (l Lock) matches(s Service) bool {
	if l.Unit != "" {
		return s.Unit == l.Unit
	}
	if l.Exe != "" {
		return s.Exe == l.Exe
	}
	return l.Program == "" || s.Program == l.Program
}

// sameService says whether two holders are the one service, as a lock would see them.
func sameService(a, b Service) bool {
	if a.Unit != "" || b.Unit != "" {
		return a.Unit == b.Unit
	}
	return a.Exe == b.Exe
}

// evaluate says how each lock's port stands, from the processes holding each port. The service's
// processes, and the ones they started that hold the socket too, hold it. Nothing else holding it
// takes it but a program the network can reach: systemd (PID 1) holding a socket for a service it
// starts on demand, a process exiting (its executable already gone), and a program on loopback alone
// are passed over.
func evaluate(locks []Lock, holders map[string][]holder) map[string]LockState {
	out := map[string]LockState{}
	for _, l := range locks {
		st := LockState{Lock: l, State: LockIdle}
		hs := holders[portKey(l.Proto, l.Port)]
		ours := map[int]bool{}
		for _, h := range hs {
			if h.pid != 1 && h.Exe != "" && l.matches(h.Service) {
				ours[h.pid] = true
			}
		}
		for grew := len(ours) > 0; grew; {
			grew = false
			for _, h := range hs {
				if !ours[h.pid] && h.pid != 1 && ours[h.ppid] {
					ours[h.pid], grew = true, true
				}
			}
		}
		if len(ours) > 0 {
			st.State = LockHeld
		}
		for _, h := range hs {
			if h.pid == 1 || h.Exe == "" || h.local || ours[h.pid] {
				continue
			}
			svc := h.Service
			st.State, st.Holder = LockTaken, &svc
			break
		}
		out[l.ID] = st
	}
	return out
}

// Locks says how each locked port stands, as the last check found it.
func (m *Manager) Locks() []LockState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lockStatesLocked()
}

// Taken are the locked ports another program holds now.
func (m *Manager) Taken() []LockState {
	var out []LockState
	for _, s := range m.Locks() {
		if s.State == LockTaken {
			out = append(out, s)
		}
	}
	return out
}

func (m *Manager) lockStatesLocked() []LockState {
	out := make([]LockState, 0, len(m.locks))
	kept := m.keptPortsLocked()
	for _, l := range m.locks {
		st, ok := m.states[l.ID]
		if !ok {
			st = LockState{State: LockIdle}
		}
		st.Lock = l
		st.WatchOnly = l.Proto == "tcp" && slices.Contains(kept, l.Port)
		out = append(out, st)
	}
	return out
}

// keptPortsLocked are the ports kept open whatever the rules: SSH's, the dashboard's, the agent's.
func (m *Manager) keptPortsLocked() []uint16 {
	var out []uint16
	for _, o := range m.openLocked() {
		out = append(out, o.Port)
	}
	return out
}

// takenLocked are the locks whose port's traffic is dropped now: taken, and not kept open.
func (m *Manager) takenLocked() []Lock {
	var out []Lock
	for _, s := range m.lockStatesLocked() {
		if s.State == LockTaken && !s.WatchOnly {
			out = append(out, s.Lock)
		}
	}
	return out
}

// Lock pins a port ("tcp" or "udp") to the service listening on it now.
func (m *Manager) Lock(ctx context.Context, port uint16, proto string) (LockState, error) {
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" {
		return LockState{}, errors.New("the protocol is tcp or udp")
	}
	if port == 0 {
		return LockState{}, errors.New("give the port to lock")
	}
	_, holders := scan(m.proc())
	svc, err := soleService(holders[portKey(proto, port)], port, proto)
	if err != nil {
		return LockState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return LockState{}, err
	}
	if slices.ContainsFunc(m.locks, func(l Lock) bool { return l.Port == port && l.Proto == proto }) {
		return LockState{}, fmt.Errorf("port %d/%s is locked already", port, proto)
	}
	if len(m.locks) >= maxLocks {
		return LockState{}, fmt.Errorf("there are %d locked ports already; unlock some first", maxLocks)
	}
	l := Lock{ID: "l" + strconv.FormatInt(m.now().UnixNano(), 36), Port: port, Proto: proto, Service: svc, Added: float64(m.now().Unix())}
	m.locks = append(slices.Clone(m.locks), l)
	m.states[l.ID] = LockState{Lock: l, State: LockHeld}
	m.watch.sig = ""
	m.saveLocked()
	log.Info("Port locked to its service", "port", port, "proto", proto, "service", svc.Name(), "exe", svc.Exe)
	return m.lockStatesLocked()[len(m.locks)-1], nil
}

// Relock pins a locked port to the service holding it now, for when the one there is the right one
// after all: a service moved to another executable, or replaced by another.
func (m *Manager) Relock(ctx context.Context, id string) (LockState, error) {
	m.mu.Lock()
	i := slices.IndexFunc(m.locks, func(l Lock) bool { return l.ID == id })
	if i < 0 {
		m.mu.Unlock()
		return LockState{}, ErrNotFound
	}
	l := m.locks[i]
	m.mu.Unlock()
	_, holders := scan(m.proc())
	svc, err := soleService(holders[portKey(l.Proto, l.Port)], l.Port, l.Proto)
	if err != nil {
		return LockState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.readyLocked(ctx); err != nil {
		return LockState{}, err
	}
	if i = slices.IndexFunc(m.locks, func(o Lock) bool { return o.ID == id }); i < 0 {
		return LockState{}, ErrNotFound
	}
	was := m.lockStatesLocked()[i]
	l.Service = svc
	m.locks = slices.Clone(m.locks)
	m.locks[i] = l
	m.states[id] = LockState{Lock: l, State: LockHeld}
	m.watch.sig = ""
	if was.State == LockTaken && !was.WatchOnly {
		if err := m.applyLocked(ctx); err != nil {
			log.Warn("Could not lift the drop on a relocked port", "err", err)
		}
	}
	m.saveLocked()
	log.Info("Port locked to the service holding it now", "port", l.Port, "proto", l.Proto, "service", svc.Name(), "exe", svc.Exe)
	return m.lockStatesLocked()[i], nil
}

// Unlock takes a lock off, and the drop it put in place with it.
func (m *Manager) Unlock(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.locks, func(l Lock) bool { return l.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	if m.problem != "" {
		return errors.New(m.problem)
	}
	was := m.lockStatesLocked()[i]
	m.locks = slices.Delete(slices.Clone(m.locks), i, i+1)
	delete(m.states, id)
	if was.State == LockTaken && !was.WatchOnly {
		if err := m.applyLocked(ctx); err != nil {
			return err
		}
	}
	m.saveLocked()
	log.Info("Port unlocked", "port", was.Port, "proto", was.Proto)
	return nil
}

// soleService is the one service listening on a port, to lock it to: the processes that are not
// helpers of another holding it, and, beside one the network reaches, not those on loopback alone.
func soleService(hs []holder, port uint16, proto string) (Service, error) {
	rs := roots(hs)
	if slices.ContainsFunc(rs, func(h holder) bool { return !h.local }) {
		rs = slices.DeleteFunc(rs, func(h holder) bool { return h.local })
	}
	var svcs []Service
	for _, h := range rs {
		if !slices.ContainsFunc(svcs, func(s Service) bool { return sameService(s, h.Service) }) {
			svcs = append(svcs, h.Service)
		}
	}
	onDemand := slices.ContainsFunc(hs, func(h holder) bool { return h.pid == 1 })
	switch {
	case len(svcs) == 1:
		return svcs[0], nil
	case len(svcs) > 1:
		names := make([]string, len(svcs))
		for i, s := range svcs {
			names[i] = s.Name()
		}
		return Service{}, fmt.Errorf("more than one program listens on port %d/%s (%s); stop the one that should not be there first", port, proto, strings.Join(names, ", "))
	case onDemand:
		return Service{}, fmt.Errorf("systemd holds port %d/%s for a service it starts when needed; lock it while that service runs", port, proto)
	}
	return Service{}, fmt.Errorf("nothing listens on port %d/%s to lock it to", port, proto)
}

// watchLocks checks the locked ports every LockEvery until ctx ends.
func (m *Manager) watchLocks(ctx context.Context) {
	m.CheckLocks(ctx)
	t := time.NewTicker(cmp.Or(m.LockEvery, 5*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.CheckLocks(ctx)
		}
	}
}

// CheckLocks reads who holds each locked port and drops, or stops dropping, what comes to those
// another program took. The holders are read again only when the locked ports' sockets changed, and
// every few checks besides, since reading them looks through every process.
func (m *Manager) CheckLocks(ctx context.Context) {
	m.mu.Lock()
	if !m.started || m.problem != "" || len(m.locks) == 0 {
		m.mu.Unlock()
		return
	}
	locks := slices.Clone(m.locks)
	wanted := map[string]bool{}
	for _, l := range locks {
		wanted[portKey(l.Proto, l.Port)] = true
	}
	var sig []string
	for _, l := range listeners(m.proc()) {
		if wanted[portKey(l.proto, l.port)] {
			sig = append(sig, portKey(l.proto, l.port)+"="+l.inode)
		}
	}
	slices.Sort(sig)
	m.watch.count++
	if s := strings.Join(sig, ","); s == m.watch.sig && m.watch.count%fullScanEvery != 0 {
		m.mu.Unlock()
		return
	} else {
		m.watch.sig = s
	}
	m.mu.Unlock()

	_, holders := scan(m.proc())
	states := evaluate(locks, holders)

	read := map[string]Lock{}
	for _, l := range locks {
		read[l.ID] = l
	}
	m.mu.Lock()
	before := slices.Clone(m.takenLocked())
	var events []LockState
	for _, l := range m.locks {
		// A lock made, or relocked to another program, since the read began is left to the next.
		if r, ok := read[l.ID]; !ok || r != l {
			continue
		}
		st := states[l.ID]
		prev, had := m.states[l.ID]
		if st.State == LockTaken {
			if prev.State == LockTaken && prev.Holder != nil && st.Holder != nil && *prev.Holder == *st.Holder {
				st.Since = prev.Since
			} else {
				st.Since = float64(m.now().Unix())
			}
		}
		m.states[l.ID] = st
		if had && (prev.State == LockTaken) != (st.State == LockTaken) || !had && st.State == LockTaken ||
			prev.State == LockTaken && st.State == LockTaken && st.Since != prev.Since {
			events = append(events, st)
		}
	}
	after := m.takenLocked()
	if !slices.EqualFunc(before, after, func(a, b Lock) bool { return a.ID == b.ID }) {
		if err := m.applyLocked(ctx); err != nil {
			log.Warn("Could not change the drops on locked ports", "err", err)
		}
	}
	// What is reported is the state with WatchOnly filled in.
	full := map[string]LockState{}
	for _, s := range m.lockStatesLocked() {
		full[s.ID] = s
	}
	for i, e := range events {
		events[i] = full[e.ID]
	}
	m.mu.Unlock()

	for _, e := range events {
		if e.State == LockTaken {
			if e.WatchOnly {
				log.Warn("A locked port was taken by another program; it is kept open, so only watched", "what", e.Words())
			} else {
				log.Warn("A locked port was taken by another program; dropping what comes to it", "what", e.Words())
			}
		} else {
			log.Info("A locked port is back to its service, or free", "what", e.Words())
		}
		if m.OnLock != nil {
			m.OnLock(e)
		}
	}
}
