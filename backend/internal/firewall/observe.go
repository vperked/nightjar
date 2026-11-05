package firewall

import (
	"context"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// hitLine is a counted line of iptables-save -c in a chain of ours, tagged with its rule.
var hitLine = regexp.MustCompile(`^\[(\d+):(\d+)\] -A NIGHTJAR-(?:RULES|OUT) .*--comment "?nj:([0-9a-z]+)"? .*-j (?:DROP|ACCEPT)`)

// countHits adds what each rule caught, from iptables-save -c, to hits.
func countHits(save string, hits map[string]Hits) {
	for line := range strings.Lines(save) {
		m := hitLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		p, _ := strconv.ParseUint(m[1], 10, 64)
		b, _ := strconv.ParseUint(m[2], 10, 64)
		h := hits[m[3]]
		h.Packets += p
		h.Bytes += b
		hits[m[3]] = h
	}
}

// maxDrops is how many dropped packets Status shows.
const maxDrops = 100

// drops reads the latest packets the rules dropped from the kernel log, newest first.
func (m *Manager) drops(ctx context.Context, rules []Rule) ([]Drop, string) {
	out, err := m.run(ctx, nil, "journalctl", "-k", "-o", "short-unix", "--no-pager", "-n", "5000")
	if err != nil {
		return nil, "The kernel log could not be read here, so dropped traffic is only counted."
	}
	return ParseDrops(out, rules), ""
}

// ParseDrops reads the kernel's log lines of dropped packets (journalctl -k -o short-unix), newest
// first, naming each by its rule's ID.
func ParseDrops(log string, rules []Rule) []Drop {
	byShort := map[string]string{"default": "default"}
	for _, r := range rules {
		byShort[short(r.ID)] = r.ID
	}
	var out []Drop
	for line := range strings.Lines(log) {
		i := strings.Index(line, " "+LogPrefix)
		if i < 0 {
			continue
		}
		rest := line[i+1+len(LogPrefix):]
		tag, fields, _ := strings.Cut(rest, " ")
		id, ok := byShort[tag]
		if !ok {
			continue
		}
		at, _ := strconv.ParseFloat(strings.Fields(line)[0], 64)
		d := Drop{At: at, Rule: id}
		var in, outIf string
		for f := range strings.FieldsSeq(fields) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "IN":
				in = v
			case "OUT":
				outIf = v
			case "SRC":
				d.Source = v
			case "DST":
				d.Dest = v
			case "PROTO":
				d.Proto = strings.ToLower(v)
			case "DPT":
				n, _ := strconv.ParseUint(v, 10, 16)
				d.Port = uint16(n)
			}
		}
		if in == "" && outIf != "" {
			d.Direction = "out"
		}
		if d.Source == "" {
			continue
		}
		out = append(out, d)
	}
	slices.Reverse(out)
	if len(out) > maxDrops {
		out = out[:maxDrops]
	}
	return out
}
