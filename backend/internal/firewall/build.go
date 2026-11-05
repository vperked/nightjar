package firewall

import (
	"fmt"
	"strconv"
	"strings"
)

// LogPrefix starts the kernel log line of each packet a rule drops: "NJ:<end of the rule's ID> ", or
// "NJ:default " for what the default drops.
const LogPrefix = "NJ:"

// logEvery is how many drops a minute each rule writes to the kernel log, so a flood cannot fill it.
const logEvery = "--limit 10/min --limit-burst 5"

// Build is the iptables-restore input for the rules of one family, IPv6 when six: each chain in use,
// its rules in order, the first match deciding. A rule for one address goes only to its family, and
// paused rules are left out.
//
// Incoming, a rule that blocks writes a sample of what it drops to the kernel log first. With the
// default "drop", what no rule decided is dropped and logged too, except replies, ping, DHCP and the
// open ports, which go on to the machine's own firewall. Outgoing, rules only look at new
// connections, so the replies to whoever reached this machine always go out.
func Build(rules []Rule, def string, open []uint16, six bool) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	var in, out []Rule
	for _, r := range rules {
		if r.Paused || (r.Source != "" && strings.Contains(r.Source, ":") != six) {
			continue
		}
		if r.Out() {
			out = append(out, r)
		} else {
			in = append(in, r)
		}
	}
	needIn := def == "drop"
	needOut := false
	for _, r := range rules {
		if !r.Paused {
			needIn = needIn || !r.Out()
			needOut = needOut || r.Out()
		}
	}
	w("*filter")
	if needIn {
		w(":%s - [0:0]", Chain)
	}
	if needOut {
		w(":%s - [0:0]", OutChain)
	}
	if needIn {
		w("-A %s -i lo -j RETURN", Chain)
		for _, r := range in {
			lines(w, Chain, r, six)
		}
		if def == "drop" {
			w("-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN", Chain)
			if six {
				w("-A %s -p ipv6-icmp -j RETURN", Chain)
				w("-A %s -p udp --dport 546 -j RETURN", Chain)
			} else {
				w("-A %s -p icmp -j RETURN", Chain)
				w("-A %s -p udp --sport 67 --dport 68 -j RETURN", Chain)
			}
			for i := 0; i < len(open); i += 15 {
				ports := make([]string, 0, 15)
				for _, p := range open[i:min(i+15, len(open))] {
					ports = append(ports, strconv.Itoa(int(p)))
				}
				w("-A %s -p tcp -m multiport --dports %s -j RETURN", Chain, strings.Join(ports, ","))
			}
			w(`-A %s -m limit %s -j LOG --log-prefix "%sdefault "`, Chain, logEvery, LogPrefix)
			w("-A %s -m comment --comment nj:default -j DROP", Chain)
		}
	}
	if needOut {
		w("-A %s -o lo -j RETURN", OutChain)
		for _, r := range out {
			lines(w, OutChain, r, six)
		}
	}
	w("COMMIT")
	return b.String()
}

// lines writes a rule's lines: one for each protocol it covers, after a log line when it blocks.
func lines(w func(string, ...any), chain string, r Rule, six bool) {
	target := "DROP"
	if r.Action == "allow" {
		target = "ACCEPT"
	}
	who := ""
	if r.Source != "" {
		flag := "-s"
		if r.Out() {
			flag = "-d"
		}
		who = flag + " " + r.Source + " "
	}
	protos := []string{r.Proto}
	if r.Proto == "" && r.Port != "" {
		protos = []string{"tcp", "udp"}
	}
	for _, p := range protos {
		match := who
		if p != "" {
			match += "-p " + p + " "
		}
		if strings.Contains(r.Port, ",") {
			match += "-m multiport --dports " + r.Port + " "
		} else if r.Port != "" {
			match += "--dport " + r.Port + " "
		}
		if r.Out() {
			match += "-m conntrack --ctstate NEW "
		}
		if r.Action == "limit" {
			match += limitMatch(r, p, six)
		}
		// A limit is not logged: a second rule with its match would count its packets twice.
		if r.Action == "block" && r.ID != "" {
			w(`-A %s %s-m limit %s -j LOG --log-prefix "%s%s "`, chain, match, logEvery, LogPrefix, short(r.ID))
		}
		tag := ""
		if r.ID != "" {
			tag = "-m comment --comment nj:" + r.ID + " "
		}
		w("-A %s %s%s-j %s", chain, match, tag, target)
	}
}

// short is the end of a rule's ID that its log lines carry: the log prefix is short.
func short(id string) string {
	if len(id) > 10 {
		return id[len(id)-10:]
	}
	return id
}

// limitMatch is the hashlimit match for a limit rule: per address, new connections over tcp and
// packets over udp, each rule with a table of its own. For IPv6 an address is a /64, which is what one
// machine has to send from.
func limitMatch(r Rule, proto string, six bool) string {
	n, unit, _ := strings.Cut(r.Rate, "/")
	burst, _ := strconv.Atoi(n)
	if unit == "sec" {
		burst *= 2
	}
	burst = min(max(burst, 1), 10000)
	m := ""
	if proto == "tcp" {
		m = "-m conntrack --ctstate NEW "
	}
	mask := ""
	if six {
		mask = "--hashlimit-srcmask 64 "
	}
	return m + fmt.Sprintf("-m hashlimit --hashlimit-above %s --hashlimit-burst %d --hashlimit-mode srcip %s--hashlimit-name nj%s%s ", r.Rate, burst, mask, short(r.ID), proto[:1])
}
