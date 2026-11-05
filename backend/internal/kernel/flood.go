package kernel

// A kernel stuck repeating one message (a driver in an error loop, a failing disk retried forever)
// writes thousands of lines a second. That costs CPU and disk on its own, pushes everything older
// out of the journal, and makes every journalctl query crawl. The kernel's ring buffer says how fast
// it is going for the price of one dmesg call.

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FloodRate is how many kernel messages a second count as a flood: a healthy kernel writes a few a
// minute, a busy one a few a second.
const FloodRate = 200

// Flood is a kernel writing far more than it should, and what it repeats most.
type Flood struct {
	PerSecond float64
	Top       string // the message repeated most, with its numbers taken out
	TopShare  float64
}

var (
	stamp = regexp.MustCompile(`^\[\s*(\d+\.\d+)\]\s?(.*)$`)
	// numbers are what changes between repeats of one message; a PCI address (0000:01:00.0) says which
	// device it is about, and stays.
	numbers = regexp.MustCompile(`[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]|0x[0-9a-fA-F]+|[0-9a-fA-F]{8,}|\d+`)
	pci     = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)
	// dump is what a kernel warning prints with it every time (registers, the call trace, the
	// modules loaded): repeated with it, but saying nothing about what went wrong.
	dump = regexp.MustCompile(`^(\?|<TASK>|</TASK>|-{3,}|\[ cut here|R[A-Z0-9]{1,3}:|E?FLAGS:|FS:|GS:|CS:|DS:|ES:|CR\d|DR\d|PKRU:|Code:|Call Trace|Modules linked in|CPU:|Hardware name:|Workqueue:|RIP:|ret_from_fork|Tainted:|irq event stamp|hardirqs |softirqs )`)
)

// flooding reads the ring buffer and says whether the kernel is flooding it. The buffer holds the
// newest messages; when they span only a few seconds, the kernel is writing them that fast.
func flooding() *Flood {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "dmesg").Output()
	if err != nil {
		return nil
	}
	return ParseFlood(out)
}

// ParseFlood measures a dmesg listing ("[seconds since boot] message").
func ParseFlood(out []byte) *Flood {
	var first, last float64
	n := 0
	counts := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		m := stamp.FindSubmatch(sc.Bytes())
		if m == nil {
			continue
		}
		t, err := strconv.ParseFloat(string(m[1]), 64)
		if err != nil {
			continue
		}
		if n == 0 {
			first = t
		}
		last = t
		n++
		// Stack trace lines ("? __pfx_kthread+0x10/0x10") repeat with every warning; the line that
		// says what went wrong is the one worth naming.
		msg := strings.TrimSpace(string(m[2]))
		// What a message says comes after its source ("nouveau 0000:01:00.0: timer: stalled"); a line
		// without that is a wrapped piece of a dump, like the rest of the modules list.
		if msg == "" || dump.MatchString(msg) || strings.Contains(msg, "+0x") || !strings.Contains(msg, ": ") {
			continue
		}
		counts[numbers.ReplaceAllStringFunc(msg, func(n string) string {
			if pci.MatchString(n) {
				return n
			}
			return "…"
		})]++
	}
	span := last - first
	if n < 1000 || span <= 0 {
		return nil // too little to say, or all in one instant at boot
	}
	rate := float64(n) / span
	if rate < FloodRate {
		return nil
	}
	f := &Flood{PerSecond: rate}
	total := 0
	for msg, c := range counts {
		total += c
		if c > counts[f.Top] || (c == counts[f.Top] && msg < f.Top) {
			f.Top = msg
		}
	}
	if total > 0 {
		f.TopShare = float64(counts[f.Top]) / float64(total)
	}
	return f
}
