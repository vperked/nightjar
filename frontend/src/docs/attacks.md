# Attacks

Nightjar watches for denial-of-service attacks: floods meant to knock this server, or one you watch, off the internet. It names how each attack is made, how strong it is, and which addresses are most involved, and alerts you when one starts and when it ends.

It reads the kernel's own network counters every 5 seconds, so nothing captures packets and nothing needs installing. The same watch runs inside the [agent](/docs/servers) on your other servers.

## The methods

| Method           | What the attacker does                                                                       | Counts from                                           |
| ---------------- | -------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| SYN flood        | Opens TCP connections and never finishes them, to fill the queue so visitors are turned away | 100 a second, or 512 half-open connections            |
| UDP flood        | Sprays UDP packets, often at ports nothing listens on                                        | 20,000 packets a second, or 5,000 to closed ports     |
| Ping flood       | Sends ping requests faster than they can be answered                                         | 1,000 pings a second                                  |
| Connection flood | Opens and holds a great many connections, often from a few addresses                         | 3,000 connections, or 300 from one address            |
| HTTP flood       | Sends ordinary-looking web requests many times a second                                      | 300 requests a second, counted in nginx's access logs |
| Volumetric       | Sends more traffic than the line can carry                                                   | 90% of the link's speed                               |

Nightjar also learns what is normal here. Once it has, a method must also be ten times its usual level to count. A server that always serves 500 requests a second doesn't trip the HTTP flood check, and a quiet one still trips at the floor.

An attack starts after two readings in a row show one, so a single spike never counts. It ends after half a minute of calm.

## The exact kind

The counters show a flood is UDP or TCP; the packets show exactly what kind. While an attack goes on, Nightjar reads one second of incoming packet headers every 30 seconds (never their contents) and names what most of them are. A kind must be at least a tenth of what was sampled, and at least 500 packets, to be named, so ordinary traffic in a quiet second never is.

- **TCP**, by the flags each packet carries: SYN flood, ACK flood, RST flood, FIN flood, PSH-ACK flood, TCP NULL or XMAS flood, and **SYN-ACK reflection**, which is replies to connections this server never opened, because someone sent SYNs to other servers in its name.
- **UDP amplification**, by the port the replies come from: DNS (53), NTP (123), SSDP (1900), memcached (11211), CLDAP (389), SNMP (161), chargen (19), WS-Discovery (3702), ARD (3283), DVR (37810), Ubiquiti discovery (10001), CoAP (5683), OpenVPN (1194), RIP (520) and NetBIOS (137). The attacker asks thousands of open services for a big answer in this server's name.
- **Other UDP**: a flood on one port (often a game server), or at random ports.
- **IP fragment flood**, **ping flood**, **ping reply reflection** (smurf), **ICMP unreachable flood** and **GRE flood**.

HTTP floods are named from nginx's log and the open connections:

- **HTTP GET or POST flood on one page**, often a search or a sign-in that is slow to build.
- **Cache-busting flood**: one page with a random query string each time, so no cache can answer.
- **Random URL flood**: pages that don't exist, mostly answered 404.
- **Login flood**: posts to a sign-in page, turned away with 401 or 403 (credential stuffing).
- **WordPress XML-RPC flood**: calls to `xmlrpc.php`.
- **Slowloris** and **slow POST** (R.U.D.Y.): connections held open with requests that never finish, seen as many web connections making almost no requests, or as requests nginx gave up on (408).
- When one user agent, or none, sends nearly all of it, the report says which: that is a single tool.

The page shows each kind with its share of what was sampled and what it does, above the counters' own measures. Alerts name the kinds when they are known.

## What is not an attack

Some busy moments look like floods. Nightjar checks what is running before it calls traffic an attack:

- **An install or update**, whether by `apt`, `dnf`, `snap`, `docker pull`, `pip`, `npm` and the like, or one of Nightjar's own app installs and updates. It downloads as fast as the line allows, which isn't an attack.
- **A torrent client** (qBittorrent, Transmission, Deluge, rTorrent). Most of its traffic is uTP, which runs over UDP, so heavy UDP to its own port isn't a flood. A real UDP flood mostly hits closed ports, which still counts.

These show on the page, marked as expected, with the reason.

## Where to see it

- **[Network](/network)**, under **Attacks**, for this machine: calm or under attack, each method with its strength against what is normal here and what it does, the addresses most involved, and past attacks.
- **A server's page**, for each server with the agent.

Addresses in Cloudflare's ranges are marked **Cloudflare: many visitors, never blocked**. Traffic through Cloudflare's proxy arrives from Cloudflare, so blocking those addresses would block your real visitors.

## Defending

While an attack goes on, Nightjar defends this machine in the firewall, and each agent defends its own server. It puts rules in **iptables chains of its own** (`NIGHTJAR`, and `NIGHTJAR-UDP`), sent to first from `INPUT`, for IPv4 and IPv6. Nothing else in your firewall is changed, and when the attack is over and its blocks have run out, the chains are removed. They're also removed when Nightjar starts, in case it stopped during an attack.

The rules answer what the attack is:

| The attack                                                         | What Nightjar does                                                                                                               |
| ------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------- |
| SYN flood                                                          | Drops new connections from one address beyond 50 a second (`hashlimit`). SYN cookies answer the rest.                            |
| ACK, RST, FIN, PSH-ACK, NULL or XMAS flood, SYN-ACK reflection     | Drops TCP that neither starts a connection nor belongs to one (`conntrack`).                                                     |
| DNS, NTP, memcached and other amplification, ping-reply reflection | Drops replies this machine never asked for, from the ports the packets came from. Answers to its own requests still get through. |
| UDP flood                                                          | Drops UDP from one address beyond 10,000 packets a second, and new UDP to ports nothing listens on, quietly.                     |
| Ping flood, ICMP unreachable flood                                 | Drops pings, or unreachable messages, beyond 100 a second.                                                                       |
| Connection flood, Slowloris, slow POST                             | Lets one address hold at most 300 connections, and 50 to the web server when requests are held open slowly (`connlimit`).        |
| HTTP flood                                                         | Blocks the addresses sending more than 30 requests a second.                                                                     |

It also **blocks the addresses doing the most**, for 15 minutes, renewed while they keep on: during a connection flood or a slow-request attack, one holding 300 connections; during an HTTP flood, one sending 30 web requests a second that are also a twentieth of all the requests. Half-open connections never get an address blocked, since their source is easy to forge; the SYN limit and SYN cookies deal with those. IPv6 addresses are limited and blocked by /64 network, since one machine is usually given a whole /64. Addresses that reach the web server through Cloudflare can't be blocked in the firewall, since their packets come from Cloudflare. Nightjar blocks those in nginx instead, with `deny` lines in `/etc/nginx/conf.d/nightjar-blocked.conf`, checked with `nginx -t` before nginx reloads. Set `WEB_BLOCK_FILE` to use another file, or `off` for none.

**Never limited or blocked:** this machine and its local networks (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, link-local and IPv6 unique-local), its name servers and default gateways, Cloudflare, whoever is logged in over SSH, the servers Nightjar watches, and the addresses in `DDOS_TRUSTED` (addresses or networks, separated by commas). Those go back to your own firewall untouched. An agent never blocks the dashboard that reads it, and takes more from `AGENT_TRUSTED` in its settings file.

Under **Attacks**, **Defense** shows the rules in force and what each does, the addresses blocked, why and for how long, and what can't be done here. **Unblock** lets an address back in and leaves it alone for an hour. **Turn off** stops defending; attacks are still watched and announced. On a server with the agent, the same switch and **Unblock** are under **Attacks** on its page; `AGENT_DEFEND=off` in `/etc/nightjar-agent.env` sets the default until the switch is used.

What a firewall on the server can't do:

- **A full line.** A flood bigger than the connection is lost before it reaches the server; only your provider, or a service in front such as Cloudflare, can absorb it. **Defense** says so.
- **Spoofed addresses.** Most SYN and UDP floods come from forged addresses, so blocking them does little; the limits and SYN cookies are what help.
- **GRE floods** are left alone, since a tunnel this machine uses could need GRE.

Defending needs root (Nightjar and the agent run as root) and iptables; the agent installs iptables if it's missing. Rules that need connection tracking are left out when the kernel isn't tracking connections, and **Defense** says which.

## Alerts

**Under attack** goes out when an attack starts, naming its methods and the addresses most involved. **Attack over** goes out when it ends, with how long it lasted and its peak. Both go through **Attacks** in your [notification rules](/docs/notifications#rules-who-hears-what).

## When you are attacked

- **A site behind Cloudflare:** turn on **Under Attack Mode** in Cloudflare's dashboard. Cloudflare absorbs floods aimed at the name, but not traffic sent straight to this server's address.
- **Floods at this server's address:** ask your provider about its DDoS protection. Most hosts filter large floods upstream, and a home connection can't filter more than the line carries.
- **A few addresses doing most of it** (a connection or HTTP flood): Nightjar blocks them itself while the attack lasts. To keep one out for good, block it in your firewall, for example `sudo ufw deny from 203.0.113.7`.
