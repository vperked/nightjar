package agent_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nightjar/internal/agent"
)

const tok = "a-token-that-is-long-enough-1234"

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The test binary itself is a Linux program for this machine, so it stands in for a new build.
func newBuild(t *testing.T) []byte {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAgentsTakeANewBuildOnlyWhenItChecksOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightjar-agent")
	os.WriteFile(path, []byte("old build"), 0o755)
	restarted := make(chan struct{}, 1)
	self := &agent.Self{Path: path, Hash: hashOf([]byte("old build")), Restart: func() { restarted <- struct{}{} }}
	s := agent.NewSampler("test")
	s.Sample(t.Context())
	h := agent.Handler(s, tok, self, true)
	post := func(body []byte, hash, token string, handler http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, agent.UpdatePath, strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(agent.BuildHeader, hash)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// Readings say which build it runs and that it can take another.
	req := httptest.NewRequest(http.MethodGet, agent.Path, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var r agent.Readings
	json.NewDecoder(rec.Body).Decode(&r)
	if r.Build != self.Hash || !r.CanUpdate {
		t.Fatalf("readings: build %q, can update %v", r.Build, r.CanUpdate)
	}

	build := newBuild(t)
	if rec := post(build, hashOf(build), "wrong-token-wrong-token-wrong", h); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", rec.Code)
	}
	if rec := post(build, hashOf(build), tok, agent.Handler(s, tok, self, false)); rec.Code != http.StatusForbidden {
		t.Fatalf("over plain HTTP: %d", rec.Code)
	}
	if rec := post(build, hashOf([]byte("something else")), tok, h); rec.Code != http.StatusBadRequest {
		t.Fatalf("a build that does not match its hash: %d", rec.Code)
	}
	script := []byte("#!/bin/sh\nrm -rf /\n")
	if rec := post(script, hashOf(script), tok, h); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a Linux program") {
		t.Fatalf("not a program: %d %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(path); string(got) != "old build" {
		t.Fatal("a refused build replaced the agent")
	}

	if rec := post(build, hashOf(build), tok, h); rec.Code != http.StatusOK {
		t.Fatalf("a good build: %d %s", rec.Code, rec.Body)
	}
	if got, _ := os.ReadFile(path); hashOf(got) != hashOf(build) || self.Hash != hashOf(build) {
		t.Fatal("the new build is not in place")
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("it did not restart on the new build")
	}

	// An agent that cannot write its own file (installed before this) says so.
	ro := &agent.Self{Path: "/proc/version", Hash: "x"}
	if rec := post(build, hashOf(build), tok, agent.Handler(s, tok, ro, true)); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "reinstall") {
		t.Fatalf("cannot update itself: %d %s", rec.Code, rec.Body)
	}
}

func TestInstallOpensTheFirewallAndAddsSmartmontools(t *testing.T) {
	var ran []string
	have := map[string]bool{"ufw": true, "apt-get": true}
	restore := agent.SetCommands(func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if name == "ufw" && args[0] == "status" {
			return "Status: active", nil
		}
		return "", nil
	}, func(name string) (string, error) {
		if have[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	})
	defer restore()
	out := &strings.Builder{}
	if fw := agent.OpenPort(":9101", out); fw != agent.FirewallUFW {
		t.Fatalf("firewall: %q", fw)
	}
	agent.InstallSmart(out)
	agent.ClosePort(agent.FirewallUFW, ":9101", out)
	want := []string{"ufw status", "ufw allow 9101/tcp comment nightjar-agent", "apt-get install -y -q smartmontools", "ufw delete allow 9101/tcp"}
	if strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("ran:\n%s", strings.Join(ran, "\n"))
	}
	if !strings.Contains(out.String(), "Opened port 9101/tcp in ufw") || !strings.Contains(out.String(), "Installed smartmontools") {
		t.Fatalf("said: %s", out)
	}

	// With smartctl there, and no firewall on, nothing is run but the looks.
	ran = nil
	have = map[string]bool{"smartctl": true}
	if fw := agent.OpenPort(":9101", out); fw != "" {
		t.Fatalf("no firewall: %q", fw)
	}
	agent.InstallSmart(out)
	if len(ran) != 0 {
		t.Fatalf("ran: %v", ran)
	}
	if env := agent.Env(":9101", tok, false, agent.FirewallUFW); !strings.Contains(env, "AGENT_FIREWALL=ufw\n") {
		t.Fatalf("the env remembers the firewall to close: %s", env)
	}
}

func TestInstallOpensPlainIPTablesAndKeepsTheRule(t *testing.T) {
	rules := map[string]int{} // tool → how many of the agent's rules it holds
	var ran []string
	restore := agent.SetCommands(func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		switch {
		case name == "iptables" && args[0] == "-S":
			return "-P INPUT DROP\n-A INPUT -i lo -j ACCEPT\n-A INPUT -p tcp --dport 22 -j ACCEPT", nil
		case args[0] == "-C":
			if rules[name] == 0 {
				return "", errors.New("Bad rule")
			}
		case args[0] == "-I":
			rules[name]++
		case args[0] == "-D":
			if rules[name] == 0 {
				return "", errors.New("Bad rule")
			}
			rules[name]--
		}
		return "", nil
	}, func(name string) (string, error) {
		switch name {
		case "iptables", "ip6tables", "netfilter-persistent":
			return "/usr/sbin/" + name, nil
		}
		return "", errors.New("not found")
	})
	defer restore()
	out := &strings.Builder{}
	if fw := agent.OpenPort(":9101", out); fw != agent.FirewallIPTables {
		t.Fatalf("firewall: %q (%s)", fw, out)
	}
	if rules["iptables"] != 1 || rules["ip6tables"] != 1 {
		t.Fatalf("rules: %v", rules)
	}
	joined := strings.Join(ran, "\n")
	for _, want := range []string{"iptables -I INPUT -p tcp --dport 9101 -m comment --comment nightjar-agent -j ACCEPT", "netfilter-persistent save"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in:\n%s", want, joined)
		}
	}
	// Installing again adds nothing; uninstalling takes the rules out and saves that.
	agent.OpenPort(":9101", out)
	if rules["iptables"] != 1 {
		t.Fatalf("a second install added another rule: %v", rules)
	}
	ran = nil
	agent.ClosePort(agent.FirewallIPTables, ":9101", out)
	if rules["iptables"] != 0 || rules["ip6tables"] != 0 || !strings.Contains(strings.Join(ran, "\n"), "netfilter-persistent save") {
		t.Fatalf("after uninstall: %v\n%s", rules, strings.Join(ran, "\n"))
	}

	// Rules that let everything in need no hole.
	restore2 := agent.SetCommands(func(name string, args ...string) (string, error) {
		if name == "iptables" && args[0] == "-S" {
			return "-P INPUT ACCEPT", nil
		}
		t.Fatalf("ran %s %v on an open firewall", name, args)
		return "", nil
	}, func(name string) (string, error) {
		if name == "iptables" {
			return "/usr/sbin/iptables", nil
		}
		return "", errors.New("not found")
	})
	defer restore2()
	if fw := agent.OpenPort(":9101", out); fw != "" {
		t.Fatalf("opened %q in a firewall that lets everything in", fw)
	}
}

func TestUpdatesArriveOneAtATimeAsAWholeBuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nightjar-agent")
	os.WriteFile(path, []byte("old build"), 0o755)
	self := &agent.Self{Path: path, Hash: hashOf([]byte("old build"))}
	build := newBuild(t)
	// Two dashboards pushing at once, while readings ask which build it runs.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { errs <- self.Apply(build, hashOf(build)) })
	}
	wg.Go(func() {
		for range 100 {
			self.Build()
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(path); hashOf(got) != hashOf(build) || self.Build() != hashOf(build) {
		t.Fatal("the new build is not in place")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o755 {
		t.Fatalf("the program's mode: %v", st.Mode())
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("files left beside the program: %v", left)
	}
}

func TestAReinstallKeepsTheSettingsAndChoicesOfTheFirstInstall(t *testing.T) {
	// Installed with -firewall=false -smart=false; the admin then added settings of their own.
	first := agent.Settings{Listen: ":9101", Token: tok, Firewall: agent.FirewallOff, NoSmart: true}.Env() +
		"AGENT_TRUSTED=203.0.113.0/24\n# the office\nAGENT_DEFEND=off\n"
	saved, ok := agent.ParseSettings(first)
	if !ok || saved.Listen != ":9101" || saved.Token != tok || saved.Plain || saved.Firewall != agent.FirewallOff || !saved.NoSmart {
		t.Fatalf("saved: %+v", saved)
	}
	if opt := saved.Reinstall(agent.Options{Firewall: true, Smart: true}); opt.Firewall || opt.Smart {
		t.Fatalf("a reinstall undid what the install was told: %+v", opt)
	}
	// Written again, the file says the same, with the admin's lines kept once each.
	again := saved.Env()
	if again != first {
		t.Fatalf("written again:\n%s\nwas:\n%s", again, first)
	}
	if got, _ := agent.ParseSettings(again); strings.Join(got.Other, "|") != "AGENT_TRUSTED=203.0.113.0/24|# the office|AGENT_DEFEND=off" {
		t.Fatalf("other settings: %q", got.Other)
	}

	// An install that opened its port in ufw, from before these were kept, reinstalls as it was.
	old, _ := agent.ParseSettings(agent.Env(":9101", tok, true, agent.FirewallUFW))
	if opt := old.Reinstall(agent.Options{Firewall: true, Smart: true}); !opt.Firewall || !opt.Smart || !old.Plain || old.Firewall != agent.FirewallUFW || len(old.Other) != 0 {
		t.Fatalf("an older install: %+v %+v", old, opt)
	}
}
