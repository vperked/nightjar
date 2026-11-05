package ddos

// Counters say a flood is UDP; only the packets say it is DNS amplification rather than a random-port
// flood, or that a TCP flood is ACKs rather than SYNs. During an attack the watcher reads one second of
// incoming packet headers (never their contents) and works out the exact kind.

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"net"
	"slices"
	"syscall"
	"time"
)

// Kind is an exact type of flood, with how much of the sampled traffic it was.
type Kind struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Share  float64 `json:"share"`            // of the packets sampled
	Detail string  `json:"detail,omitempty"` // "from port 53", "on port 27015"
	How    string  `json:"how"`
}

// reflectors are UDP services that answer a small spoofed request with a big reply: an attacker asks
// thousands of them, in the victim's name, and the replies arrive here from that port.
var reflectors = map[uint16][2]string{
	53:    {"DNS amplification", "Open DNS resolvers answering spoofed queries, often for large records, in this server's name."},
	123:   {"NTP amplification", "NTP servers answering spoofed monlist requests, each reply hundreds of times the request."},
	1900:  {"SSDP amplification", "Home routers and media devices answering spoofed UPnP discovery requests."},
	11211: {"Memcached amplification", "Exposed memcached servers answering spoofed requests, with replies up to tens of thousands of times larger."},
	389:   {"CLDAP amplification", "Windows domain controllers answering spoofed LDAP-over-UDP queries."},
	19:    {"Chargen amplification", "Old chargen services sending floods of characters in reply to spoofed requests."},
	161:   {"SNMP amplification", "Network devices answering spoofed SNMP requests with long tables."},
	3702:  {"WS-Discovery amplification", "Cameras and printers answering spoofed WS-Discovery probes."},
	3283:  {"ARD amplification", "Macs with Apple Remote Desktop answering spoofed requests."},
	37810: {"DVR amplification", "Video recorders answering spoofed DHDiscover requests."},
	10001: {"Ubiquiti discovery amplification", "Ubiquiti devices answering spoofed discovery requests."},
	5683:  {"CoAP amplification", "Internet-of-things devices answering spoofed CoAP requests."},
	1194:  {"OpenVPN reflection", "OpenVPN servers answering spoofed handshake packets."},
	520:   {"RIP amplification", "Routers answering spoofed RIP requests."},
	137:   {"NetBIOS amplification", "Windows machines answering spoofed NetBIOS name queries."},
}

// Profile is what a sample of packets held.
type Profile struct {
	Packets   int
	Bytes     int
	TCP       int
	UDP       int
	ICMP      int
	GRE       int
	Fragments int
	TCPFlags  map[string]int // "SYN", "SYN-ACK", "ACK", "RST", "FIN", "PSH-ACK", "NULL", "XMAS", "other"
	UDPSrc    map[uint16]int
	UDPDst    map[uint16]int
	ICMPTypes map[uint8]int
	Sources   map[string]int
}

func newProfile() *Profile {
	return &Profile{TCPFlags: map[string]int{}, UDPSrc: map[uint16]int{}, UDPDst: map[uint16]int{}, ICMPTypes: map[uint8]int{}, Sources: map[string]int{}}
}

// Add parses one Ethernet frame's headers.
func (p *Profile) Add(frame []byte) {
	if len(frame) < 14 {
		return
	}
	etype := binary.BigEndian.Uint16(frame[12:14])
	off := 14
	for etype == 0x8100 || etype == 0x88a8 { // VLAN tags
		if len(frame) < off+4 {
			return
		}
		etype = binary.BigEndian.Uint16(frame[off+2 : off+4])
		off += 4
	}
	pkt := frame[off:]
	var proto int
	var src net.IP
	var l4 []byte
	fragment := false
	switch etype {
	case 0x0800:
		if len(pkt) < 20 {
			return
		}
		ihl := int(pkt[0]&0x0f) * 4
		if ihl < 20 || len(pkt) < ihl {
			return
		}
		proto = int(pkt[9])
		src = net.IP(pkt[12:16])
		flagsFrag := binary.BigEndian.Uint16(pkt[6:8])
		fragment = flagsFrag&0x2000 != 0 || flagsFrag&0x1fff != 0
		if flagsFrag&0x1fff == 0 {
			l4 = pkt[ihl:] // only the first fragment carries the ports
		}
	case 0x86dd:
		if len(pkt) < 40 {
			return
		}
		proto = int(pkt[6])
		src = net.IP(pkt[8:24])
		l4 = pkt[40:]
		if proto == 44 && len(l4) >= 8 { // fragment header
			fragment = true
			proto = int(l4[0])
			if binary.BigEndian.Uint16(l4[2:4])&0xfff8 == 0 {
				l4 = l4[8:]
			} else {
				l4 = nil
			}
		}
	default:
		return
	}
	p.Packets++
	p.Bytes += len(frame)
	p.Sources[src.String()]++
	if fragment {
		p.Fragments++
	}
	switch proto {
	case 6:
		p.TCP++
		if len(l4) >= 14 {
			p.TCPFlags[tcpFlags(l4[13])]++
		}
	case 17:
		p.UDP++
		if len(l4) >= 4 {
			p.UDPSrc[binary.BigEndian.Uint16(l4[0:2])]++
			p.UDPDst[binary.BigEndian.Uint16(l4[2:4])]++
		}
	case 1, 58:
		p.ICMP++
		if len(l4) >= 1 {
			p.ICMPTypes[l4[0]]++
		}
	case 47:
		p.GRE++
	}
}

func tcpFlags(f byte) string {
	const fin, syn, rst, psh, ack, urg = 0x01, 0x02, 0x04, 0x08, 0x10, 0x20
	switch {
	case f&0x3f == 0:
		return "NULL"
	case f&(fin|psh|urg) == fin|psh|urg:
		return "XMAS"
	case f&(syn|ack) == syn:
		return "SYN"
	case f&(syn|ack) == syn|ack:
		return "SYN-ACK"
	case f&rst != 0:
		return "RST"
	case f&fin != 0:
		return "FIN"
	case f&(psh|ack) == psh|ack:
		return "PSH-ACK"
	case f&ack != 0:
		return "ACK"
	}
	return "other"
}

var tcpKinds = map[string][2]string{
	"SYN":     {"SYN flood", "Connection requests that are never completed, to fill the queue of half-open connections."},
	"SYN-ACK": {"SYN-ACK reflection", "Replies to connections this server never opened: the attacker sent SYNs to other servers in this server's name."},
	"ACK":     {"ACK flood", "Packets acknowledging connections that do not exist, to tire out firewalls and the kernel looking them up."},
	"RST":     {"RST flood", "Resets for connections that do not exist, to make the kernel look each one up."},
	"FIN":     {"FIN flood", "Connection-closing packets for connections that do not exist."},
	"PSH-ACK": {"PSH-ACK flood", "Data pushed on connections that do not exist, often with junk payloads."},
	"NULL":    {"TCP NULL flood", "TCP packets with no flags set at all, which no real connection sends."},
	"XMAS":    {"TCP XMAS flood", "TCP packets with every flag lit at once, which no real connection sends."},
}

// minKindPackets is the fewest packets of one kind in a one-second sample to call it a flood.
const minKindPackets = 500

// Kinds works out the exact types of flood in a profile, the biggest first. A kind must be at least a
// tenth of the packets, and minKindPackets of them, to be named.
func (p *Profile) Kinds() []Kind {
	if p.Packets == 0 {
		return nil
	}
	total := float64(p.Packets)
	var out []Kind
	add := func(id, name, how, detail string, n int) {
		// A large share of a quiet moment is ordinary traffic; a flood is many packets as well.
		if share := float64(n) / total; share >= 0.1 && n >= minKindPackets {
			out = append(out, Kind{ID: id, Name: name, How: how, Detail: detail, Share: share})
		}
	}
	for flags, n := range p.TCPFlags {
		if k, ok := tcpKinds[flags]; ok {
			add("tcp_"+flags, k[0], k[1], "", n)
		}
	}
	// UDP: replies from one service's port are amplification; the rest a plain flood, at one port or many.
	reflected := 0
	for port, n := range p.UDPSrc {
		if r, ok := reflectors[port]; ok {
			add(fmt.Sprintf("udp_reflect_%d", port), r[0], r[1], fmt.Sprintf("from port %d", port), n)
			reflected += n
		}
	}
	if plain := p.UDP - reflected; plain > 0 {
		port, most := topPort(p.UDPDst)
		if float64(most) >= 0.6*float64(plain) {
			add("udp_port", "UDP flood on one port", "UDP packets aimed at one service, to overwhelm it.", fmt.Sprintf("on port %d", port), plain)
		} else {
			add("udp_random", "UDP flood (random ports)", "UDP packets sprayed at ports at random, most of them closed, so the kernel answers each with an error.", "", plain)
		}
	}
	add("fragments", "IP fragment flood", "Packets broken into fragments that must be held and put back together, to use up memory and CPU.", "", p.Fragments)
	add("icmp_echo", "Ping flood", "Echo requests faster than they can be answered.", "", p.ICMPTypes[8]+p.ICMPTypes[128])
	add("icmp_reply", "Ping reply reflection", "Echo replies to pings this server never sent: others were pinged in its name.", "", p.ICMPTypes[0]+p.ICMPTypes[129])
	add("icmp_unreach", "ICMP unreachable flood", "Destination-unreachable messages, to make the kernel process a stream of errors.", "", p.ICMPTypes[3]+p.ICMPTypes[1])
	add("gre", "GRE flood", "Tunnelled GRE packets, used by botnets to carry large floods.", "", p.GRE)
	slices.SortFunc(out, func(a, b Kind) int { return cmp.Compare(b.Share, a.Share) })
	return out
}

func topPort(m map[uint16]int) (uint16, int) {
	var port uint16
	most := 0
	for p, n := range m {
		if n > most || (n == most && p < port) {
			port, most = p, n
		}
	}
	return port, most
}

// Capture reads incoming packets' headers on an interface for up to d, or until limit packets.
func Capture(iface string, d time.Duration, limit int) (*Profile, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, err
	}
	proto := htons(syscall.ETH_P_ALL)
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(proto))
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: ifi.Index}); err != nil {
		return nil, err
	}
	tv := syscall.NsecToTimeval((100 * time.Millisecond).Nanoseconds())
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	p := newProfile()
	buf := make([]byte, 256) // the headers are all that is wanted
	deadline := time.Now().Add(d)
	for p.Packets < limit && time.Now().Before(deadline) {
		n, from, err := syscall.Recvfrom(fd, buf, syscall.MSG_TRUNC)
		if err != nil {
			continue // a timeout: look at the clock again
		}
		if ll, ok := from.(*syscall.SockaddrLinklayer); ok && ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		p.Add(buf[:min(n, len(buf))])
		p.Bytes += n - min(n, len(buf)) // the packet's full size, of which only the headers were read
	}
	return p, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
