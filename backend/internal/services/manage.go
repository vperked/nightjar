package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"nightjar/internal/storage"
)

var (
	ErrNotFound = errors.New("no check with that name")
	ErrExists   = errors.New("a check with that name already exists")
)

// RunSoon has a check run again on the next cycle, after something was done about it.
func (m *Monitor) RunSoon(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[name]; ok {
		st.runNow = true
	}
}

// Specs returns every check's definition, in display order.
func (m *Monitor) Specs() []Spec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Spec, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.states[name].Spec)
	}
	return out
}

// Save adds a check (oldName "") or replaces the one called oldName, renaming it if spec.Name
// differs. It is stored first, so a check is never running without being saved.
// A changed target or type starts the check's status over; it runs on the next cycle.
func (m *Monitor) Save(oldName string, spec Spec) error {
	if m.store == nil {
		return storage.ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if oldName != "" {
		if _, ok := m.states[oldName]; !ok {
			return ErrNotFound
		}
	}
	if _, taken := m.states[spec.Name]; taken && spec.Name != oldName {
		return ErrExists
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err := m.store.SaveMonitor(oldName, spec.Name, data); err != nil {
		return err
	}
	if oldName == "" {
		m.order = append(m.order, spec.Name)
		m.states[spec.Name] = m.restore(spec)
		return nil
	}
	st := m.states[oldName]
	if st.Spec.Type != spec.Type || st.Spec.Target != spec.Target {
		// A run still under way checked the old target: it stays running, so the new one does not
		// start beside it, and its result is dropped. runNow then runs the new one straight after.
		*st = State{Spec: spec, Status: StatusUnknown, history: st.history, slots: st.slots, running: st.running, stale: st.running}
	}
	st.Spec, st.runNow = spec, true
	if oldName != spec.Name {
		delete(m.states, oldName)
		m.states[spec.Name] = st
		m.order[slices.Index(m.order, oldName)] = spec.Name
		if st.running {
			// Its result will come under the old name.
			if m.renamed == nil {
				m.renamed = map[string]string{}
			}
			// Renamed again before it reported: the result still comes under the first name.
			chained := false
			for from, to := range m.renamed {
				if to == oldName {
					m.renamed[from], chained = spec.Name, true
				}
				if m.renamed[from] == from {
					delete(m.renamed, from) // renamed back
				}
			}
			if !chained {
				m.renamed[oldName] = spec.Name
			}
		}
	}
	return nil
}

// Delete removes a check and its history.
func (m *Monitor) Delete(name string) error {
	if m.store == nil {
		return storage.ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[name]; !ok {
		return ErrNotFound
	}
	if err := m.store.DeleteMonitor(name); err != nil {
		return err
	}
	delete(m.states, name)
	m.order = slices.DeleteFunc(m.order, func(n string) bool { return n == name })
	return nil
}

// Stored returns the checks saved in store. While it has none, the checks in importFile (a
// services.json from before checks were managed in the dashboard) are imported into it once.
func Stored(store *storage.Store, importFile string, warn func(string)) []Spec {
	var specs []Spec
	for _, data := range store.Monitors() {
		spec, err := ParseSpec(data)
		if err != nil {
			warn(fmt.Sprintf("skipping stored check: %v", err))
			continue
		}
		specs = append(specs, spec)
	}
	if len(specs) > 0 || importFile == "" {
		return specs
	}
	if _, err := os.Stat(importFile); err != nil {
		return nil
	}
	for _, spec := range Load(importFile, warn) {
		data, _ := json.Marshal(spec)
		if err := store.SaveMonitor("", spec.Name, data); err != nil {
			warn(fmt.Sprintf("could not import %s: %v", spec.Name, err))
			continue
		}
		specs = append(specs, spec)
	}
	if len(specs) > 0 {
		warn(fmt.Sprintf("imported %d check(s) from %s; they are now managed in the dashboard", len(specs), importFile))
	}
	return specs
}
