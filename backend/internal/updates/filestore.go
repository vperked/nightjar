package updates

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"nightjar/internal/storage/record"
)

// FileStore keeps targets in one JSON file, for the agent, which has no database. Every change
// rewrites the whole file (a few kilobytes) through a temporary file and a rename, so a crash or a
// full disk leaves the last whole version in place, never half of one. Only root can read it: a
// target can hold an app's API key.
type FileStore struct {
	path string

	mu  sync.Mutex
	doc fileDoc
}

type fileDoc struct {
	Targets []fileTarget      `json:"targets"`
	Meta    map[string]string `json:"meta,omitempty"`
	// Settings belong to whoever runs the manager (the agent's schedule); kept here so there is one file.
	Settings json.RawMessage `json:"settings,omitempty"`
}

type fileTarget struct {
	ID    string          `json:"id"`
	Spec  json.RawMessage `json:"spec"`
	State json.RawMessage `json:"state,omitempty"`
}

// OpenFileStore reads the file at path; a missing file is an empty store. A file that cannot be
// read as one is moved aside (path.bad) and the store starts empty, with the error to log: the
// targets are found again by detection, which beats never updating anything.
func OpenFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return s, nil
	case err != nil:
		return s, err
	}
	if err := json.Unmarshal(raw, &s.doc); err != nil {
		s.doc = fileDoc{}
		if mvErr := os.Rename(path, path+".bad"); mvErr != nil {
			return s, fmt.Errorf("%s is not readable (%v), and could not be moved aside: %v", path, err, mvErr)
		}
		return s, fmt.Errorf("%s was not readable (%v); moved it to %s.bad and started afresh", path, err, path)
	}
	return s, nil
}

func (s *FileStore) UpdateTargets() []record.UpdateTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]record.UpdateTarget, 0, len(s.doc.Targets))
	for _, t := range s.doc.Targets {
		out = append(out, record.UpdateTarget{ID: t.ID, Spec: slices.Clone(t.Spec), State: slices.Clone(t.State)})
	}
	return out
}

func (s *FileStore) SaveUpdateTarget(id string, spec []byte) error {
	return s.change(func(d *fileDoc) {
		if i := slices.IndexFunc(d.Targets, func(t fileTarget) bool { return t.ID == id }); i >= 0 {
			d.Targets[i].Spec = slices.Clone(spec)
			return
		}
		d.Targets = append(d.Targets, fileTarget{ID: id, Spec: slices.Clone(spec)})
	})
}

func (s *FileStore) SaveUpdateState(id string, state []byte) error {
	return s.change(func(d *fileDoc) {
		if i := slices.IndexFunc(d.Targets, func(t fileTarget) bool { return t.ID == id }); i >= 0 {
			d.Targets[i].State = slices.Clone(state)
		}
	})
}

func (s *FileStore) DeleteUpdateTarget(id string) error {
	return s.change(func(d *fileDoc) {
		d.Targets = slices.DeleteFunc(d.Targets, func(t fileTarget) bool { return t.ID == id })
	})
}

func (s *FileStore) Meta(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc.Meta[key]
}

func (s *FileStore) SetMeta(key, value string) error {
	return s.change(func(d *fileDoc) {
		if d.Meta == nil {
			d.Meta = map[string]string{}
		}
		d.Meta[key] = value
	})
}

// Available is always true: a write that fails says so itself.
func (s *FileStore) Available() bool { return true }

// Settings are the settings kept with the targets, nil when none were saved.
func (s *FileStore) Settings() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.doc.Settings)
}

// SaveSettings keeps settings with the targets.
func (s *FileStore) SaveSettings(raw json.RawMessage) error {
	return s.change(func(d *fileDoc) { d.Settings = slices.Clone(raw) })
}

// change applies f and writes the file. When the write fails, the store keeps what it had, so what
// it answers matches what a restart would read.
func (s *FileStore) change(f func(*fileDoc)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc.clone()
	f(&next)
	if err := writeFileAtomic(s.path, next); err != nil {
		return err
	}
	s.doc = next
	return nil
}

// clone copies what a change may alter in place; the JSON in it is only ever replaced.
func (d fileDoc) clone() fileDoc {
	return fileDoc{Targets: slices.Clone(d.Targets), Meta: maps.Clone(d.Meta), Settings: d.Settings}
}

func writeFileAtomic(path string, doc fileDoc) error {
	if doc.Targets == nil {
		doc.Targets = []fileTarget{}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // gone already once renamed
	// CreateTemp makes it 0600 already; said again so it does not rest on that.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
