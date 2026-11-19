package agent_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nightjar/internal/agent"
	"nightjar/internal/updates"
)

// A build that does not start on this machine is turned down while the agent that can take
// another still runs, and the build replaced by one that does is kept to go back to.
func TestANewBuildMustStartAndTheOldOneIsKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightjar-agent")
	os.WriteFile(path, []byte("old build"), 0o755)
	var tried string
	self := &agent.Self{Path: path, Hash: hashOf([]byte("old build")), Starts: func(p string) error {
		tried = p
		return errors.New("the new build does not start on this machine: illegal instruction")
	}}
	build := newBuild(t)
	if err := self.Apply(build, hashOf(build)); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Fatalf("a build that does not start: %v", err)
	}
	if tried == "" || tried == path {
		t.Fatalf("the build is tried before it takes the program's place, not after: %q", tried)
	}
	if got, _ := os.ReadFile(path); string(got) != "old build" || self.Build() != hashOf([]byte("old build")) {
		t.Fatal("a build that does not start replaced the agent")
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("files left beside the program: %v", left)
	}

	self.Starts = func(string) error { return nil }
	if err := self.Apply(build, hashOf(build)); err != nil {
		t.Fatal(err)
	}
	if prev, _ := os.ReadFile(self.Previous()); string(prev) != "old build" {
		t.Fatalf("the build replaced is kept: %q", prev)
	}
}

// startsWhenRead is a request body that, as it is first read, has something begin: an update
// that starts while a build arrives over a slow link.
type startsWhenRead struct {
	r     io.Reader
	begin func()
}

func (s *startsWhenRead) Read(p []byte) (int, error) {
	if s.begin != nil {
		s.begin()
		s.begin = nil
	}
	return s.r.Read(p)
}

// Reading a build can take minutes; an update that began meanwhile is looked for again before the
// build takes the agent's place, so the restart does not cut it off.
func TestAnUpdateThatBeginsWhileABuildArrivesIsNotCutOff(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightjar-agent")
	os.WriteFile(path, []byte("old build"), 0o755)
	restarted := make(chan struct{}, 1)
	self := &agent.Self{Path: path, Hash: hashOf([]byte("old build")), Restart: func() { restarted <- struct{}{} }}
	machine := newAptMachine()
	machine.hold = make(chan struct{})
	u := agentUpdates(t, t.TempDir(), machine)
	s := agent.NewSampler("test")
	s.UseUpdates(u)
	h := agent.Handler(s, tok, self, true)

	build := newBuild(t)
	body := &startsWhenRead{r: strings.NewReader(string(build)), begin: func() {
		if err := u.Manager.Apply("plex"); err != nil {
			t.Error(err)
		}
		for deadline := time.Now().Add(5 * time.Second); u.Busy() == "" && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
	}}
	req := httptest.NewRequest(http.MethodPost, agent.UpdatePath, body)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set(agent.BuildHeader, hashOf(build))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "busy updating Plex") {
		t.Fatalf("an update begun during the read: %d %q", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "old build" {
		t.Fatal("the build was replaced while updating")
	}
	close(machine.hold)
	settleAgent(t, u, "plex", func(st updates.Status) bool { return len(st.Runs) == 1 })
	select {
	case <-restarted:
		t.Fatal("restarted although the build was turned down")
	default:
	}
}

// systemd fills in ${VAR} in a unit's command line before sh sees it, so a release tag could be
// pasted into an update command. Newer systemd is told not to; on older, each $ is doubled.
func TestUnitCommandsKeepTheirDollarSigns(t *testing.T) {
	cmd := updates.Command{Argv: []string{"sh", "-c", `echo "${TAG}" $VERSION`}, Env: []string{"TAG=v1"}}
	for version, want := range map[string]string{"255": `echo "${TAG}" $VERSION`, "250": `echo "$${TAG}" $$VERSION`} {
		dir := t.TempDir()
		run, calls := fakeSystemdRun(t, dir, version)
		r := &agent.UnitRunner{Dir: filepath.Join(dir, "logs"), SystemdRun: run, Systemctl: "true"}
		if _, err := r.Run(context.Background(), cmd); err != nil {
			t.Fatal(err)
		}
		args := readLines(t, calls)
		if got := args[len(args)-1]; got != want {
			t.Errorf("systemd %s: the script reaches systemd as %q, want %q", version, got, want)
		}
		if off := slices.Contains(args, "--expand-environment=no"); off != (version == "255") {
			t.Errorf("systemd %s: --expand-environment=no given: %v", version, off)
		}
	}
}

// At its start the agent waits for update steps it left running when it stopped (they run on in
// units of their own), then clears away their output files.
func TestTheAgentWaitsForUpdateStepsItLeftRunning(t *testing.T) {
	dir := t.TempDir()
	run, _ := fakeSystemdRun(t, dir, "255")
	count := filepath.Join(dir, "count")
	// Lists one unit still running for the first three looks.
	ctl := script(t, dir, "systemctl", "#!/bin/sh\n"+
		"n=$(cat "+count+" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "+count+"\n"+
		"if [ \"$1\" = list-units ] && [ $n -le 3 ]; then echo 'nightjar-update-apt-get-1763550000-1.service loaded active running Nightjar agent update step'; fi\n")
	logs := filepath.Join(dir, "logs")
	os.MkdirAll(logs, 0o700)
	os.WriteFile(filepath.Join(logs, "run-123.out"), []byte("left from before"), 0o600)
	r := &agent.UnitRunner{Dir: logs, SystemdRun: run, Systemctl: ctl, SettlePoll: 10 * time.Millisecond}
	r.Settle(context.Background())
	if n, _ := os.ReadFile(count); strings.TrimSpace(string(n)) != "4" {
		t.Fatalf("looked %s times, want until the unit was gone (4)", n)
	}
	if left, _ := os.ReadDir(logs); len(left) != 0 {
		t.Fatalf("output files from before are cleared away: %v", left)
	}

	// One that never ends is waited for only as long as the context allows.
	forever := script(t, dir, "systemctl-forever", "#!/bin/sh\necho 'nightjar-update-sh-1-1.service loaded active running x'\n")
	r = &agent.UnitRunner{Dir: logs, SystemdRun: run, Systemctl: forever, SettlePoll: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r.Settle(ctx)
	if time.Since(start) > 5*time.Second {
		t.Fatal("Settle outlasted its context")
	}
}
