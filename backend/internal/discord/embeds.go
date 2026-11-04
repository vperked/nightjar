package discord

import (
	"cmp"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"nightjar/internal/collect"
	"nightjar/internal/format"
	"nightjar/internal/metrics"
)

// The embeds read alike: the title says what happened with its number ("CPU at 93%"), the
// description says why it matters in a sentence, and a few short fields give the rest. The limit
// that was crossed is said once, in the description, never again as a field.

const footer = ""

func f(name, value string, inline bool) Field { return Field{Name: name, Value: value, Inline: inline} }

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v) }

func bytesU(v uint64) string { return format.Bytes(float64(v)) }

// Span is a short length of time: "42s", "3m 12s", "2h 5m", "3d 4h".
func Span(seconds float64) string {
	s := int64(seconds + 0.5)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		if s%60 == 0 {
			return fmt.Sprintf("%dm", s/60)
		}
		return fmt.Sprintf("%dm %ds", s/60, s%60)
	case s < 86400:
		if s%3600/60 == 0 {
			return fmt.Sprintf("%dh", s/3600)
		}
		return fmt.Sprintf("%dh %dm", s/3600, s%3600/60)
	}
	if s%86400/3600 == 0 {
		return fmt.Sprintf("%dd", s/86400)
	}
	return fmt.Sprintf("%dd %dh", s/86400, s%86400/3600)
}

// List is lines of "name · value", for a field that lists a few things.
func List(lines []string) string { return strings.Join(lines, "\n") }

// top is the busiest processes as "name · 12%" lines.
func top(ps []collect.Process, n int, mem bool) []string {
	var lines []string
	for _, p := range ps {
		if len(lines) >= n {
			break
		}
		v := p.CPUPercent
		if mem {
			v = p.MemoryPercent
		}
		if v >= 1 {
			lines = append(lines, fmt.Sprintf("%s · %.0f%%", p.Name, v))
		}
	}
	return lines
}

// CPUAlert reports overall usage and the busiest cores.
func CPUAlert(s Sender, cpuPercent, threshold float64, perCore []float64) bool {
	var fields []Field
	desc := fmt.Sprintf("Above the %s limit.", pct(threshold))
	if len(perCore) > 0 {
		desc = fmt.Sprintf("Above the %s limit, across %d cores.", pct(threshold), len(perCore))
		idx := make([]int, len(perCore))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return perCore[idx[a]] > perCore[idx[b]] })
		var lines []string
		for _, i := range idx[:min(4, len(idx))] {
			lines = append(lines, fmt.Sprintf("Core %d · %.0f%%", i, perCore[i]))
		}
		fields = append(fields, f("Busiest cores", strings.Join(lines, "\n"), false))
	}
	return s.SendEmbed("CPU at "+pct(cpuPercent), desc, Warning, fields, footer)
}

// MemoryAlert reports RAM and swap usage.
func MemoryAlert(s Sender, percent, threshold float64, memory, swap collect.Memory) bool {
	fields := []Field{f("Used", bytesU(memory.Used), true), f("Available", bytesU(memory.Available), true)}
	if swap.Total > 0 {
		fields = append(fields, f("Swap", fmt.Sprintf("%s of %s", bytesU(swap.Used), bytesU(swap.Total)), true))
	}
	return s.SendEmbed("Memory at "+pct(percent), fmt.Sprintf("Above the %s limit, with %s of %s free.", pct(threshold), bytesU(memory.Available), bytesU(memory.Total)), Warning, fields, footer)
}

// GPUAlert reports GPU utilization, memory, temperature and power.
func GPUAlert(s Sender, gpu collect.GPU, threshold float64) bool {
	fields := []Field{f("Memory", fmt.Sprintf("%s of %s", format.Megabytes(gpu.MemUsedMB), format.Megabytes(gpu.MemTotalMB)), true)}
	if gpu.Temperature > 0 {
		fields = append(fields, f("Temperature", fmt.Sprintf("%.0f°C", gpu.Temperature), true))
	}
	if gpu.PowerDraw > 0 {
		fields = append(fields, f("Power", fmt.Sprintf("%.0f W of %.0f W", gpu.PowerDraw, gpu.PowerLimit), true))
	}
	desc := fmt.Sprintf("Above the %s limit.", pct(threshold))
	if gpu.Name != "" {
		desc = fmt.Sprintf("%s is above the %s limit.", gpu.Name, pct(threshold))
	}
	return s.SendEmbed("GPU at "+pct(gpu.Utilization), desc, Warning, fields, footer)
}

// TemperatureDetails is what the temperature alert explains the heat with.
type TemperatureDetails struct {
	Temperature, Threshold, Duration, CPUPercent float64
	Reason                                       string
	Freq                                         collect.CPUFreq
	TopProcesses                                 []collect.Process
	Fans                                         []collect.Fan
}

func TemperatureAlert(s Sender, d TemperatureDetails) bool {
	desc := fmt.Sprintf("Above the %.0f°C limit", d.Threshold)
	if d.Duration > 0 {
		desc += " for " + Span(d.Duration)
	}
	desc += "."
	fields := []Field{f("CPU", pct(d.CPUPercent), true)}
	if d.Freq.Current > 0 {
		fields = append(fields, f("Clock", fmt.Sprintf("%.1f GHz", d.Freq.Current/1000), true))
	}
	var fans []string
	for _, fan := range d.Fans[:min(3, len(d.Fans))] {
		fans = append(fans, fmt.Sprintf("%s · %d rpm", cmp.Or(fan.Label, fan.Name), fan.RPM))
	}
	if len(fans) > 0 {
		fields = append(fields, f("Fans", List(fans), true))
	}
	if d.Reason != "" {
		fields = append(fields, f("Likely why", List(strings.Split(d.Reason, " | ")), false))
	}
	if procs := top(d.TopProcesses, 5, false); len(procs) > 0 {
		fields = append(fields, f("Busiest", List(procs), false))
	}
	return s.SendEmbed(fmt.Sprintf("CPU at %.0f°C", d.Temperature), desc, Warning, fields, footer)
}

// ProcessAlert reports one process over a CPU or memory threshold.
func ProcessAlert(s Sender, p collect.Process, resource string, threshold float64) bool {
	name, value := "CPU", p.CPUPercent
	if resource == "memory" {
		name, value = "memory", p.MemoryPercent
	}
	mem := pct(p.MemoryPercent)
	if p.RSS > 0 {
		mem += " · " + bytesU(p.RSS)
	}
	fields := []Field{f("PID", fmt.Sprint(p.PID), true), f("CPU", pct(p.CPUPercent), true), f("Memory", mem, true)}
	if p.Username != "" {
		fields = append(fields, f("User", p.Username, true))
	}
	return s.SendEmbed(fmt.Sprintf("%s is using %s %s", p.Name, pct(value), name), fmt.Sprintf("Above the %s limit for one process.", pct(threshold)), Warning, fields, footer)
}

// Subdirectory is one entry in the directory size alert.
type Subdirectory struct {
	Path string
	Size int64
}

func DirectorySizeAlert(s Sender, dir string, size, threshold int64, subdirs []Subdirectory) bool {
	var lines []string
	for _, sd := range subdirs[:min(5, len(subdirs))] {
		lines = append(lines, fmt.Sprintf("%s · %s", filepath.Base(sd.Path), format.Bytes(float64(sd.Size))))
	}
	var fields []Field
	if len(lines) > 0 {
		fields = append(fields, f("Largest inside", List(lines), false))
	}
	return s.SendEmbed(fmt.Sprintf("%s is %s", dir, format.Bytes(float64(size))),
		fmt.Sprintf("%s over its %s limit.", format.Bytes(float64(size-threshold)), format.Bytes(float64(threshold))),
		Warning, fields, "Directory Monitoring")
}

func PartitionAlert(s Sender, p collect.Partition, threshold float64, earlyWarning bool) bool {
	title := fmt.Sprintf("%s is %s full", p.Mountpoint, pct(p.Percent))
	desc := fmt.Sprintf("%s free of %s. The limit is %s.", bytesU(p.Free), bytesU(p.Total), pct(threshold))
	if earlyWarning {
		title = fmt.Sprintf("%s is filling up: %s", p.Mountpoint, pct(p.Percent))
		desc = fmt.Sprintf("%s free of %s, nearing the %s limit.", bytesU(p.Free), bytesU(p.Total), pct(threshold))
	}
	kind := p.Fstype
	if p.IsNVMe {
		kind = strings.TrimSpace("NVMe " + kind)
	}
	fields := []Field{f("Device", p.Device, true), f("Type", kind, true)}
	return s.SendEmbed(title, desc, Warning, fields, "Storage Monitoring")
}

// CleanedLog is one log file in the batched cleanup alert.
type CleanedLog struct {
	Path                      string
	OriginalSize, CurrentSize string
}

func LogCleanupAlert(s Sender, logs []CleanedLog, threshold string) bool {
	if len(logs) == 0 {
		return false
	}
	title := "Cleaned a large log"
	if len(logs) > 1 {
		title = fmt.Sprintf("Cleaned %d large logs", len(logs))
	}
	var lines []string
	for i, l := range logs {
		if i >= 15 {
			lines = append(lines, fmt.Sprintf("and %d more", len(logs)-15))
			break
		}
		if l.OriginalSize != "" {
			lines = append(lines, fmt.Sprintf("`%s` · %s → %s", l.Path, l.OriginalSize, l.CurrentSize))
		} else {
			lines = append(lines, fmt.Sprintf("`%s` · %s", l.Path, l.CurrentSize))
		}
	}
	return s.SendEmbed(title, fmt.Sprintf("Each was over %s, so it was emptied.", threshold), Info, []Field{f("Logs", cap1024(List(lines)), false)}, "Storage Monitoring")
}

// FreezeDetails is the freeze alert's input.
type FreezeDetails struct {
	Reasons            []string
	SinceHeartbeat     float64
	Responsive         bool
	UptimeFormatted    string
	CPUPercent, MemPct float64
}

func FreezeAlert(s Sender, d FreezeDetails) bool {
	desc := "The system stopped responding."
	if len(d.Reasons) > 0 {
		desc = strings.TrimSuffix(d.Reasons[0], ".") + "."
	}
	fields := []Field{f("Last heartbeat", Span(d.SinceHeartbeat)+" ago", true), f("Responding now", map[bool]string{true: "Yes", false: "No"}[d.Responsive], true), f("Last CPU and memory", pct(d.CPUPercent)+" · "+pct(d.MemPct), true)}
	if len(d.Reasons) > 1 {
		fields = append(fields, f("Also seen", List(d.Reasons[1:min(4, len(d.Reasons))]), false))
	}
	return s.SendEmbed("The system froze", desc, Critical, fields, footer)
}

// SystemInfo is the startup embed.
func SystemInfo(s Sender, sys collect.System, cpuPercent float64, cores int, memory collect.Memory, disk collect.DiskTotal, procs collect.ProcessOverview) bool {
	os := strings.TrimSpace(cmp.Or(sys.PlatformVersion, sys.Platform+" "+sys.PlatformRelease))
	desc := fmt.Sprintf("Watching %s, up %s.", cmp.Or(os, "this machine"), Span(float64(sys.UptimeSeconds)))
	fields := []Field{
		f("CPU", fmt.Sprintf("%s of %d cores", pct(cpuPercent), cores), true),
		f("Memory", fmt.Sprintf("%s of %s", bytesU(memory.Used), bytesU(memory.Total)), true),
		f("Disk", pct(disk.Percent)+" used", true),
	}
	if lines := top(procs.TopCPU, 3, false); len(lines) > 0 {
		fields = append(fields, f("Busiest", List(lines), true))
	}
	if lines := top(procs.TopMemory, 3, true); len(lines) > 0 {
		fields = append(fields, f("Most memory", List(lines), true))
	}
	return s.SendEmbed("Nightjar started", desc, Info, fields, "Monitor")
}

func Shutdown(s Sender, uptime, shutdown float64) bool {
	return s.SendEmbed("Nightjar stopped", "It was shut down on purpose. Nothing is watched until it starts again.", Info,
		[]Field{f("Ran for", Span(uptime), true), f("Took", fmt.Sprintf("%.1fs to stop", shutdown), true)}, "Monitor")
}

func cap1024(s string) string {
	if len(s) <= 1024 {
		return s
	}
	return s[:1021] + "..."
}

func MetricsReport(s Sender, m metrics.Summary) bool {
	sortedKeys := func(m map[string]int) []string {
		keys := make([]string, 0, len(m))
		for k, v := range m {
			if v > 0 {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] || m[keys[i]] == m[keys[j]] && keys[i] < keys[j] })
		return keys
	}
	var counts, errs, times []string
	for _, k := range sortedKeys(m.AlertCounts) {
		if len(counts) < 8 {
			counts = append(counts, fmt.Sprintf("%s · %d", k, m.AlertCounts[k]))
		}
	}
	for _, k := range sortedKeys(m.ErrorCounts) {
		if len(errs) < 8 {
			errs = append(errs, fmt.Sprintf("%s · %d", k, m.ErrorCounts[k]))
		}
	}
	timeKeys := make([]string, 0, len(m.MonitorExecutionTimesS))
	for k, v := range m.MonitorExecutionTimesS {
		if v > 0 {
			timeKeys = append(timeKeys, k)
		}
	}
	sort.Slice(timeKeys, func(i, j int) bool {
		return m.MonitorExecutionTimesS[timeKeys[i]] > m.MonitorExecutionTimesS[timeKeys[j]]
	})
	for _, k := range timeKeys[:min(5, len(timeKeys))] {
		times = append(times, fmt.Sprintf("%s · %.1f ms", k, m.MonitorExecutionTimesS[k]*1000))
	}
	fields := []Field{
		f("Alerts", fmt.Sprint(m.TotalAlerts), true),
		f("Errors", fmt.Sprint(m.TotalErrors), true),
		f("Delivered", fmt.Sprintf("%.0f%% of %d", m.WebhookSuccessRate, m.WebhookTotalCalls), true),
		f("Most alerts", List(counts), true),
		f("Errors by kind", List(errs), true),
		f("Slowest checks", List(times), true),
	}
	return s.SendEmbed("Nightjar report", fmt.Sprintf("Running for %s.", cmp.Or(m.UptimeFormatted, "a while")), Info, fields, "Metrics Report")
}
