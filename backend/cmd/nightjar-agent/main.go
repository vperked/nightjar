// nightjar-agent runs on a server the dashboard watches from another machine. Copy it over and
// run `sudo nightjar-agent install -token <token>` with the token from the dashboard's Add server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"nightjar/internal/agent"
)

var version = "dev"

const usage = `nightjar-agent reports this machine's readings to a Nightjar dashboard.

  sudo nightjar-agent install -token <token> [-listen :9101] [-tls=false] [-firewall=false] [-smart=false]
        install it as a service that starts on boot (the token comes from the dashboard);
        it serves HTTPS with a key the token pins, or plain HTTP with -tls=false. It also opens
        its port in ufw, firewalld or iptables when one is on, and installs smartmontools for drive readings,
        unless told not to. Over HTTPS, the dashboard keeps it on its own version.
  sudo nightjar-agent install
        reinstall it as it was set up (the token and settings already on this machine), so an
        older install can update itself from then on
  sudo nightjar-agent uninstall
        stop it and remove it
  nightjar-agent [-listen :9101]
        run it in the foreground, with the token in AGENT_TOKEN
  nightjar-agent version
`

func main() {
	defaultListen := ":" + strconv.Itoa(agent.DefaultPort)
	if v := os.Getenv("AGENT_LISTEN"); v != "" {
		defaultListen = v
	}
	cmd := ""
	args := os.Args[1:]
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("nightjar-agent", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	listen := fs.String("listen", defaultListen, "address to listen on: a port, or host:port")
	token := fs.String("token", os.Getenv("AGENT_TOKEN"), "the token the dashboard presents")
	useTLS := fs.Bool("tls", os.Getenv("AGENT_TLS") != "false", "serve HTTPS (the dashboard pins the key the token gives it)")
	firewall := fs.Bool("firewall", true, "on install, open the port in ufw, firewalld or iptables when one is on")
	smartTools := fs.Bool("smart", true, "on install, add smartmontools for drive readings")
	fs.Parse(args)

	var err error
	switch cmd {
	case "install":
		err = agent.Install(*listen, *token, !*useTLS, agent.Options{Firewall: *firewall, Smart: *smartTools}, os.Stdout)
	case "uninstall":
		err = agent.Uninstall(os.Stdout)
	case "version":
		fmt.Println("nightjar-agent", version)
	case "", "run":
		err = run(*listen, *token, !*useTLS)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "nightjar-agent:", err)
		os.Exit(1)
	}
}

func run(listen, token string, plain bool) error {
	listen, err := agent.ListenAddress(listen)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	slog.Info("nightjar-agent "+version+" listening", "listen", listen, "https", !plain)
	return agent.Serve(ctx, listen, token, version, plain)
}
