package discord

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"servermonitor/internal/collect"
	"servermonitor/internal/format"
	"servermonitor/internal/metrics"
)

const footer = ""

func f(name, value string, inline bool) Field { return Field{Name: name, Value: value, Inline: inline} }

func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

func bytesU(v uint64) string { return format.Bytes(float64(v)) }

// CPUAlert reports overall and per-core usage.
func CPUAlert(s Sender, cpuPercent, threshold float64, perCore []float64) bool {
	fields := []Field{f("Overall CPU Usage", pct(cpuPercent), true), f("Threshold", pct(threshold), true)}
	if len(perCore) > 0 {
		lines := make([]string, len(perCore))
		for i, u := range perCore {
			lines[i] = fmt.Sprintf("Core %d: %.1f%%", i, u)
		}
		fields = append(fields, f("Per-Core Usage", strings.Join(lines, "\n"), false))
	}
	return s.SendEmbed("CPU Usage Alert", fmt.Sprintf("CPU usage has exceeded the threshold of %.1f%%", threshold), Warning, fields, footer)
}

// MemoryAlert reports RAM and swap usage.
func MemoryAlert(s Sender, percent, threshold float64, memory, swap collect.Memory) bool {
	fields := []Field{
		f("Memory Usage", pct(percent), true),
		f("Threshold", pct(threshold), true),
		f("Memory Details", fmt.Sprintf("Used: %s\nAvailable: %s\nTotal: %s", bytesU(memory.Used), bytesU(memory.Available), bytesU(memory.Total)), false),
	}
	if swap.Total > 0 {
		fields = append(fields, f("Swap Memory", fmt.Sprintf("Used: %s (%.1f%%)\nTotal: %s", bytesU(swap.Used), swap.Percent, bytesU(swap.Total)), true))
	}
	return s.SendEmbed("Memory Usage Alert", fmt.Sprintf("Memory usage has exceeded the threshold of %.1f%%", threshold), Warning, fields, footer)
}

// GPUAlert reports GPU utilization, memory, temperature and power.
func GPUAlert(s Sender, gpu collect.GPU, threshold float64) bool {
	fields := []Field{
		f("GPU Utilization", pct(gpu.Utilization), true),
		f("Threshold", pct(threshold), true),
		f("GPU Info", fmt.Sprintf("%s\nDriver: %s", gpu.Name, gpu.DriverVersion), false),
		f("GPU Memory", fmt.Sprintf("%.1f%% (%s / %s)", gpu.MemPercent, format.Megabytes(gpu.MemUsedMB), format.Megabytes(gpu.MemTotalMB)), true),
	}
	if gpu.Temperature > 0 {
		fields = append(fields, f("Temperature", fmt.Sprintf("%.1f°C", gpu.Temperature), true))
	}
	if gpu.PowerDraw > 0 {
		fields = append(fields, f("Power", fmt.Sprintf("%.1fW / %.1fW", gpu.PowerDraw, gpu.PowerLimit), true))
	}
	return s.SendEmbed("GPU Usage Alert", fmt.Sprintf("GPU utilization has exceeded the threshold of %.1f%%", threshold), Warning, fields, footer)
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
	durationStr := fmt.Sprintf("%.0fs", d.Duration)
	if d.Duration >= 60 {
		durationStr = fmt.Sprintf("%.1fmin", d.Duration/60)
	}
	fields := []Field{
		f("CPU Temperature", fmt.Sprintf("%.1f°C", d.Temperature), true),
		f("Threshold", fmt.Sprintf("%.1f°C", d.Threshold), true),
		f("Duration Above Threshold", durationStr, true),
	}
	if d.Reason != "" {
		fields = append(fields, f("Likely Cause", d.Reason, false))
	}
	fields = append(fields, f("CPU Usage", pct(d.CPUPercent), true))
	if d.Freq.Current > 0 {
		freqPct := 0.0
		if d.Freq.Max > 0 {
			freqPct = d.Freq.Current / d.Freq.Max * 100
		}
		fields = append(fields, f("CPU Frequency", fmt.Sprintf("%.0f MHz (%.0f%% of max)", d.Freq.Current, freqPct), true))
	}
	var procs []string
	for i, p := range d.TopProcesses {
		if i >= 5 {
			break
		}
		if p.CPUPercent > 1 {
			procs = append(procs, fmt.Sprintf("%s: %.1f%%", p.Name, p.CPUPercent))
		}
	}
	if len(procs) > 0 {
		fields = append(fields, f("Top CPU Processes", strings.Join(procs, "\n"), false))
	}
	var fans []string
	for i, fan := range d.Fans {
		if i >= 3 {
			break
		}
		fans = append(fans, fmt.Sprintf("%s: %d RPM", fan.Label, fan.RPM))
	}
	if len(fans) > 0 {
		fields = append(fields, f("Fan Speeds", strings.Join(fans, "\n"), false))
	}
	desc := fmt.Sprintf("CPU temperature has exceeded the threshold of %.1f°C", d.Threshold)
	if d.Duration > 0 {
		desc += fmt.Sprintf(" for %.0f seconds", d.Duration)
	}
	return s.SendEmbed("Temperature Alert", desc, Warning, fields, footer)
}

// ProcessAlert reports one process over a CPU or memory threshold.
func ProcessAlert(s Sender, p collect.Process, resource string, threshold float64) bool {
	name, value := "CPU", p.CPUPercent
	if resource == "memory" {
		name, value = "Memory", p.MemoryPercent
	}
	fields := []Field{
		f("Process", fmt.Sprintf("%s (PID: %d)", p.Name, p.PID), false),
		f(name+" Usage", pct(value), true),
		f("Threshold", pct(threshold), true),
		f("Resource Usage", fmt.Sprintf("CPU: %.1f%%\nMemory: %.1f%%", p.CPUPercent, p.MemoryPercent), true),
	}
	if p.RSS > 0 || p.VMS > 0 {
		fields = append(fields, f("Memory Details", fmt.Sprintf("RSS: %s\nVMS: %s", bytesU(p.RSS), bytesU(p.VMS)), true))
	}
	if p.Username != "" {
		fields = append(fields, f("User", p.Username, true))
	}
	return s.SendEmbed(fmt.Sprintf("Process %s Alert", name),
		fmt.Sprintf("Process %q has exceeded the %s threshold of %.1f%%", p.Name, strings.ToLower(name), threshold),
		Warning, fields, footer)
}

// Subdirectory is one entry in the directory size alert.
type Subdirectory struct {
	Path string
	Size int64
}

func DirectorySizeAlert(s Sender, dir string, size, threshold int64, subdirs []Subdirectory) bool {
	fields := []Field{
		f("Directory Path", dir, false),
		f("Current Size", format.Bytes(float64(size)), true),
		f("Threshold", format.Bytes(float64(threshold)), true),
		f("Over Threshold By", format.Bytes(float64(size-threshold)), true),
	}
	var lines []string
	for i, sd := range subdirs {
		if i >= 5 {
			break
		}
		lines = append(lines, fmt.Sprintf("%s: %s", filepath.Base(sd.Path), format.Bytes(float64(sd.Size))))
	}
	if len(lines) > 0 {
		fields = append(fields, f("Largest Subdirectories", strings.Join(lines, "\n"), false))
	}
	return s.SendEmbed("Directory Size Alert",
		fmt.Sprintf("Directory %q has exceeded the size threshold of %s", filepath.Base(dir), format.Bytes(float64(threshold))),
		Warning, fields, "Directory Monitoring")
}

func PartitionAlert(s Sender, p collect.Partition, threshold float64, earlyWarning bool) bool {
	title, verb, drive := "Drive Usage Alert", "exceeding", "Drive"
	if earlyWarning {
		title, verb = "Drive Early Warning", "approaching"
	}
	if p.IsNVMe {
		title, drive = "NVMe "+title, "NVMe drive"
	}
	fields := []Field{
		f("Device", p.Device, true), f("Mount Point", p.Mountpoint, true),
		f("Usage", pct(p.Percent), true), f("Threshold", pct(threshold), true),
		f("Total Space", bytesU(p.Total), true), f("Used Space", bytesU(p.Used), true), f("Free Space", bytesU(p.Free), true),
	}
	if p.IsNVMe {
		fields = append(fields, f("Drive Type", "NVMe SSD", true))
	}
	if p.Fstype != "" {
		fields = append(fields, f("File System", p.Fstype, true))
	}
	return s.SendEmbed(title, fmt.Sprintf("%s usage is %s threshold", drive, verb), Warning, fields, "Storage Monitoring")
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
	desc := "Log file exceeded threshold and was auto-cleaned"
	if len(logs) > 1 {
		desc = fmt.Sprintf("%d log files exceeded threshold and were auto-cleaned", len(logs))
	}
	var lines []string
	for i, l := range logs {
		if i >= 20 {
			lines = append(lines, fmt.Sprintf("\n*... and %d more log file(s)*", len(logs)-20))
			break
		}
		if l.OriginalSize != "" {
			lines = append(lines, fmt.Sprintf("• `%s` - %s -> %s", l.Path, l.OriginalSize, l.CurrentSize))
		} else {
			lines = append(lines, fmt.Sprintf("• `%s` - %s", l.Path, l.CurrentSize))
		}
	}
	fields := []Field{
		f("Logs Cleaned", fmt.Sprint(len(logs)), true), f("Threshold", threshold, true), f("Status", "Auto-cleaned", true),
		f("Cleaned Log Files", strings.Join(lines, "\n"), false),
	}
	return s.SendEmbed("Log Size Alert - Batch Cleanup", desc, Info, fields, "Storage Monitoring")
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
	var fields []Field
	primary := "System freeze detected"
	if len(d.Reasons) > 0 {
		primary = d.Reasons[0]
		var lines []string
		for i, r := range d.Reasons {
			if i >= 3 {
				break
			}
			lines = append(lines, "• "+r)
		}
		fields = append(fields, f("Detection Reason", strings.Join(lines, "\n"), false))
	}
	responsive := "No"
	if d.Responsive {
		responsive = "Yes"
	}
	fields = append(fields,
		f("Time Since Last Heartbeat", fmt.Sprintf("%.1f seconds", d.SinceHeartbeat), true),
		f("System Responsive", responsive, true),
	)
	if d.UptimeFormatted != "" {
		fields = append(fields, f("System Uptime", d.UptimeFormatted, true))
	}
	fields = append(fields, f("Last Known CPU Usage", pct(d.CPUPercent), true), f("Last Known Memory Usage", pct(d.MemPct), true))
	return s.SendEmbed("System Freeze Alert", "System freeze has been detected: "+primary, Critical, fields, footer)
}

// SystemInfo is the startup embed.
func SystemInfo(s Sender, sys collect.System, cpuPercent float64, cores int, memory collect.Memory, disk collect.DiskTotal, procs collect.ProcessOverview) bool {
	fields := []Field{
		f("System", fmt.Sprintf("%s %s\nHostname: %s", sys.Platform, sys.PlatformRelease, sys.Hostname), false),
		f("Uptime", sys.UptimeFormatted, true),
		f("CPU", fmt.Sprintf("%.1f%%\n%d cores", cpuPercent, cores), true),
		f("Memory", fmt.Sprintf("%.1f%%\n%s / %s", memory.Percent, bytesU(memory.Used), bytesU(memory.Total)), true),
		f("Disk", fmt.Sprintf("%.1f%% used", disk.Percent), true),
		f("Processes", fmt.Sprintf("%d total", procs.Total), true),
	}
	list := func(ps []collect.Process, mem bool) string {
		var lines []string
		for i, p := range ps {
			if i >= 3 {
				break
			}
			v := p.CPUPercent
			if mem {
				v = p.MemoryPercent
			}
			lines = append(lines, fmt.Sprintf("%s: %.1f%%", p.Name, v))
		}
		return strings.Join(lines, "\n")
	}
	if len(procs.TopCPU) > 0 {
		fields = append(fields, f("Top CPU Processes", list(procs.TopCPU, false), true))
	}
	if len(procs.TopMemory) > 0 {
		fields = append(fields, f("Top Memory Processes", list(procs.TopMemory, true), true))
	}
	return s.SendEmbed("System Information", "Comprehensive system status and statistics", Info, fields, "Monitor")
}

func Shutdown(s Sender, uptime, shutdown float64) bool {
	status := "Slow shutdown"
	if shutdown < 1 {
		status = "Fast shutdown"
	} else if shutdown < 3 {
		status = "Normal shutdown"
	}
	fields := []Field{
		f("Monitoring Duration", format.Duration(uptime), true),
		f("Shutdown Time", format.Duration(shutdown), true),
		f("Status", status, true),
	}
	return s.SendEmbed("Server Monitor Stopped", "Server monitoring has been stopped by user.", Info, fields, "Monitor")
}

func cap1024(s string) string {
	if len(s) <= 1024 {
		return s
	}
	return s[:1021] + "..."
}

func MetricsReport(s Sender, m metrics.Summary) bool {
	text := func(items []string) string {
		if len(items) == 0 {
			return "None"
		}
		if len(items) > 10 {
			items = items[:10]
		}
		return cap1024(strings.Join(items, "\n"))
	}
	sortedKeys := func(m map[string]int) []string {
		keys := make([]string, 0, len(m))
		for k, v := range m {
			if v > 0 {
				keys = append(keys, k)
			}
		}
		sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
		return keys
	}
	var freq, counts, errs, times []string
	freqKeys := make([]string, 0, len(m.AlertFrequencyPerHour))
	for k := range m.AlertFrequencyPerHour {
		freqKeys = append(freqKeys, k)
	}
	sort.Strings(freqKeys)
	for _, k := range freqKeys {
		if v := m.AlertFrequencyPerHour[k]; v > 0 {
			freq = append(freq, fmt.Sprintf("%s: %.1f/hr", k, v))
		}
	}
	for _, k := range sortedKeys(m.AlertCounts) {
		counts = append(counts, fmt.Sprintf("%s: %d", k, m.AlertCounts[k]))
	}
	for _, k := range sortedKeys(m.ErrorCounts) {
		errs = append(errs, fmt.Sprintf("%s: %d", k, m.ErrorCounts[k]))
	}
	timeKeys := make([]string, 0, len(m.MonitorExecutionTimesS))
	for k := range m.MonitorExecutionTimesS {
		timeKeys = append(timeKeys, k)
	}
	sort.Strings(timeKeys)
	for _, k := range timeKeys {
		if v := m.MonitorExecutionTimesS[k]; v > 0 {
			times = append(times, fmt.Sprintf("%s: %.2fms", k, v*1000))
		}
	}
	fields := []Field{
		f("Uptime", m.UptimeFormatted, true),
		f("Total Alerts", fmt.Sprint(m.TotalAlerts), true),
		f("Total Errors", fmt.Sprint(m.TotalErrors), true),
		f("Webhook Stats", fmt.Sprintf("Success Rate: %.1f%%\nTotal: %d\nSuccess: %d\nFailure: %d",
			m.WebhookSuccessRate, m.WebhookTotalCalls, m.WebhookSuccess, m.WebhookFailure), false),
		f("Alert Frequency (per hour)", text(freq), false),
		f("Alert Counts", text(counts), false),
	}
	if len(errs) > 0 {
		fields = append(fields, f("Error Counts", text(errs), false))
	}
	if len(times) > 0 {
		fields = append(fields, f("Avg Execution Times", text(times), false))
	}
	return s.SendEmbed("Server Monitor Metrics Report", "Performance and health metrics for the monitoring system", Info, fields, "Metrics Report")
}
