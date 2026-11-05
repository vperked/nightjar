package agent

// The dashboard keeps its agents on its own build: when one reports a different build, the dashboard
// sends its own (POST UpdatePath, over the HTTPS the token pins, with the token). The agent takes it
// only if it is whole (its SHA-256 matches), is a Linux program for this machine's processor, and the
// agent can replace itself, which an install from this version on can: it runs from its own state
// folder. It then swaps the file in and exits, and systemd starts the new build; an agent run by hand
// starts the new build in its own place instead.

import (
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
)

// UpdatePath is where the dashboard sends a new build.
const UpdatePath = "/v1/update"

// BuildHeader carries the SHA-256 of the build being sent.
const BuildHeader = "X-Agent-Build"

// maxBuild is the most an update may be; the agent is about 8 MB.
const maxBuild = 64 << 20

// Self is the running agent's own file, for updating it.
type Self struct {
	Path string // the agent program
	Hash string // its SHA-256, hex; read it with Build while the agent serves
	// Restart is called once a new build is in place; the default exits so systemd starts it, or
	// runs the new build in this process's place when nothing would start it again.
	Restart func()

	// mu keeps one update at a time, and Hash with the file it names.
	mu sync.Mutex
}

// Build is the SHA-256 of the build in place.
func (s *Self) Build() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Hash
}

// FindSelf hashes the running program.
func FindSelf() (*Self, error) {
	p, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return nil, err
	}
	h, err := hashFile(p)
	if err != nil {
		return nil, err
	}
	return &Self{Path: p, Hash: h}, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CanUpdate says whether the agent can replace its own file: its folder must be writable to it.
func (s *Self) CanUpdate() bool {
	if s == nil {
		return false
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".write-test-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// machines are the ELF machine types for each processor the agent is built for.
var machines = map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}

// Apply checks a build and puts it in place of the running one.
func (s *Self) Apply(build []byte, hash string) error {
	sum := sha256.Sum256(build)
	if hex.EncodeToString(sum[:]) != hash {
		return errors.New("the build does not match its hash")
	}
	f, err := elf.NewFile(bytes.NewReader(build))
	if err != nil {
		return errors.New("that is not a Linux program")
	}
	defer f.Close()
	// ET_DYN covers programs built as position-independent executables.
	if want, ok := machines[runtime.GOARCH]; !ok || f.Machine != want || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return fmt.Errorf("that build is not for this machine (%s)", runtime.GOARCH)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hash == s.Hash {
		return nil
	}
	// A file of its own beside the program, written out to the disk before it takes the program's
	// place, so the program is only ever the whole of one build.
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), "."+filepath.Base(s.Path)+".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(build); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
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
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return err
	}
	s.Hash = hash
	return nil
}

// supervised says whether a service manager starts the agent again when it exits: systemd sets
// INVOCATION_ID for the services it runs.
func supervised() bool {
	return os.Getenv("INVOCATION_ID") != ""
}

// restart starts the new build: by exiting under systemd, which starts it again (Restart=always),
// else in this process's place, so an agent run by hand keeps running.
func (s *Self) restart() {
	if supervised() {
		os.Exit(0)
	}
	if err := syscall.Exec(s.Path, os.Args, os.Environ()); err != nil {
		// It runs on as it was; the new build starts the next time it does.
		slog.Error("could not start the new build", "path", s.Path, "err", err)
	}
}

// updateHandler takes a new build from the dashboard. Only over HTTPS: over plain HTTP anyone on the
// way could swap in a program of their own.
func updateHandler(self *Self, authorized func(*http.Request) bool, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case !secure:
			http.Error(w, "this agent serves plain HTTP; reinstall it with HTTPS to have it update itself", http.StatusForbidden)
			return
		case self == nil || !self.CanUpdate():
			http.Error(w, "this agent cannot replace itself; reinstall it once (sudo nightjar-agent install) to have it update itself", http.StatusConflict)
			return
		}
		// An 8 MB build over a slow link takes longer than readings do.
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
		build, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBuild))
		if err != nil {
			http.Error(w, "could not read the build: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := self.Apply(build, r.Header.Get(BuildHeader)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Write([]byte("updated; restarting\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		restart := self.Restart
		if restart == nil {
			restart = self.restart
		}
		time.AfterFunc(500*time.Millisecond, restart)
	}
}
