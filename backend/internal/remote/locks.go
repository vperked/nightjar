package remote

import (
	"strings"

	"nightjar/internal/discord"
	"nightjar/internal/firewall"
)

// locksLocked alerts once when an agent reports a locked port taken by another program, and again
// when the port is its service's again (or the lock is gone). A lock taken anew by yet another program
// is said again.
func (m *Monitor) locksLocked(e *entry, taken []firewall.LockState) {
	seen := map[string]bool{}
	for _, s := range taken {
		key := "lock:" + s.ID
		seen[key] = true
		words := "Locked port taken: " + s.Words()
		prev, known := e.problems[key]
		e.problems[key] = words
		// Words of "" are an alert from before the dashboard restarted, said already.
		if known && (prev == words || prev == "") {
			continue
		}
		m.unsaved = true
		desc := "Something other than the service it is locked to now listens on it. Nightjar drops what comes to that port until the service has it back."
		if s.WatchOnly {
			desc = "Something other than the service it is locked to now listens on it. The port is kept open for SSH or the dashboard, so Nightjar only watches it and drops nothing."
		}
		log.Warn("Locked port taken on a server", "server", e.host.Name, "what", s.Words())
		m.sendAs(e, "Locked port taken: "+e.host.Name, strings.ToUpper(s.Words()[:1])+s.Words()[1:]+". "+desc, discord.Critical, "Attacks")
	}
	for k := range e.problems {
		if !strings.HasPrefix(k, "lock:") || seen[k] {
			continue
		}
		delete(e.problems, k)
		m.unsaved = true
		log.Info("Locked port back to its service on a server", "server", e.host.Name)
		m.sendAs(e, "Locked port back: "+e.host.Name, "The port is its service's again, nothing holds it, or it was unlocked. Nightjar no longer drops what comes to it.", discord.OK, "Attacks")
	}
}
