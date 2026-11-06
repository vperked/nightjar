# Firewall

Nightjar manages the whole firewall of this machine, and of each server with the agent, under **[Network → Firewall](/network/firewall)**. Pick the machine at the top. A server's page shows its firewall in a line, with **manage →**. The page has:

- **Incoming traffic no rule decides**: left to the machine's own firewall, or dropped. See [The default](/docs/firewall#the-default).
- **Rules**, incoming and outgoing, in order, with what each caught.
- **Open ports**: each port something listens on, the program holding it, and whether anyone may reach it.
- **Dropped lately**: the latest packets the rules and the default dropped, and who was dropped most.
- **The machine's own rules**, as ufw, firewalld or iptables lists them, read only.

## Rules

Rules are checked in order, and the first that matches decides. Use **↑** and **↓** to move one. A new block or limit goes in before the allows, so an allow can't lift it; a new allow goes last.

**+ Add a rule** opens a form that asks one thing at a time:

- **Traffic**: **Coming in** to this machine, or **Going out** from it.
- **Do**: **Allow** lets it in (even where the machine's own firewall would drop it), **Block** drops it, and **Limit** lets each address make only so many new connections and drops the rest. Nothing is picked for you.
- **From** (or **To**, for an outgoing rule): **Anyone**, or **Only** an address (`203.0.113.7`) or a network (`203.0.113.0/24`, or an IPv6 one).
- **Port**: **Every port**, or **Only** a port (`22`), a range (`8000-8100`) or a list (`80,443`, up to 15 ports, a range counting as two), over tcp and udp or just one of them.
- **At most** (for a limit): how many a second, a minute or an hour from each address. A limit is always for a port.
- **Note**: why, for later.

At the foot, the form says the rule in words (**This rule will limit anyone on tcp port 22 to 6 a minute each.**) or, until it is complete, what is still missing. A rule for anyone on every port is refused there, since it would block or let in everything.

**Quick** fills in an allow for what a home server usually runs: SSH, the web, Plex, qBittorrent (its own ports), DNS and Samba. **Add rule** beside an open port opens the form on its port, and **Block** beside an address that kept being dropped opens it on a block of that address.

Each rule says what it caught since the rules were last changed: **dropped 1,204 · 70 KB**, or **let in** for an allow. **Pause** takes a rule out of the firewall but keeps it, to try things without losing it; **Resume** puts it back. **Edit** changes it in place, checked like a new one.

### Outgoing rules

Under **Outgoing**, rules decide which new connections this machine may open: block one to stop it reaching an address or a port (mail on port 25, say), and allow one above a block to make an exception. They only look at new connections, so the replies to whoever reached this machine always go out. Limits are for incoming traffic only.

## Rate limits

A limit lets each address open so many new connections to a service (over tcp), or send so many packets (over udp), and drops the rest until it slows down. Ordinary use stays well under it; a flood or a password-guessing script doesn't:

- **SSH**: 6 a minute on its port stops password guessing, and a person logging in never notices.
- **A web app or API** that something hammers: 20 a second on its port.
- **A game or DNS server** over udp: a few hundred packets a second each.

Each address is counted on its own, so one busy address can't use up everyone's share. Limits use iptables' `hashlimit`.

## The default

**Incoming traffic no rule decides** is either:

- **Leave it to ufw** (or firewalld, or plain iptables): the machine's own firewall decides, as before. This is where every machine starts.
- **Drop it**: only what a rule allows gets in.

Dropping keeps open what must stay open: **SSH**, **the dashboard's ports** (80, 443 and its own here; the agent's on a server), **ping**, DHCP, and **replies** to what the machine sent. Those go on to the machine's own firewall as before.

Before dropping, Nightjar lists the open ports no rule lets anyone reach, which dropping would close (**closed by the default** under **Open ports**). Tick those to keep open and it adds an allow rule for each.

The change is made at once and **goes back by itself after a minute** unless you press **Keep it**. If it cut the dashboard off, the page can't answer, nothing keeps it, and the machine is back as it was a minute later. **Undo** puts it back at once. A restart before it is kept comes back with the old default too.

Docker's published ports go through the forwarding chain, not this one, so the default doesn't close them; block them with rules on the machine itself if needed.

## What was dropped

Blocks and the default write what they drop to the kernel log, at most 10 a minute for each rule, so a flood can't fill it. **Dropped lately** reads the latest of them: when, from where, to which port, and which rule. Limits aren't logged (their count is enough). **Dropped most** lists the addresses that keep coming back, with **Block** to fill in a rule for one; those a block already covers say **blocked**.

The counts start again whenever the rules change. On a machine without journald, the drops are counted but not listed.

## What Nightjar refuses

A rule that would lock you out is turned down, with the reason:

- **A block on the dashboard**: on a server, the dashboard's address on the agent's port; here, your own address on the web ports.
- **A block on whoever is logged in over SSH** now, on the SSH port.
- **A limit under 6 a minute** on the ports the dashboard or an SSH login uses.
- **Blocking everyone from every port**, **allowing everyone to every port**, or **a rule for all outgoing traffic**: give an address or a port.

Editing a rule, or resuming a paused one, is checked the same way.

## How it works

The rules live in iptables chains of Nightjar's own, for IPv4 and IPv6: `NIGHTJAR-RULES`, first in `INPUT`, and `NIGHTJAR-OUT`, first in `OUTPUT`. So a block here stops traffic ufw would let in, and an allow here lets in traffic ufw would drop. Nothing else in the firewall is changed. Nightjar keeps the rules and the default and puts them back when it starts, so they last across reboots. With nothing left in a chain, it is taken away.

Rules saved before rules had an order keep doing what they did: blocks first, then limits, then allows.

## Defending from here

On a server's page, **Defense** under **Attacks** has its switch and **Unblock** too: they reach the agent, the same as here. See [Attacks](/docs/attacks#defending).

## Needs

Managing a server's firewall needs its agent on this version (agents update themselves; an older one shows its rules only, and says so) and serving HTTPS: over plain HTTP the agent shows its firewall but changes nothing. Nightjar and the agent run as root, and need iptables; the agent installs it if it's missing. On a machine using the older iptables-legacy, reinstall the agent once (**Reinstall agent** under **SSH**) so it may take iptables' lock.

Every change is written to the [activity log](/docs/security#the-activity-log).
