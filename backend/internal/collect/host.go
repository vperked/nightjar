// Package collect reads host metrics: CPU, memory, disks, network, GPU, sensors, system and processes.
package collect

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// CPUFreq is in MHz.
type CPUFreq struct {
	Current float64 `json:"current"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
}

// PerCore returns per-core usage since the previous call (the first call returns zeros).
func PerCore() ([]float64, error) {
	return cpu.Percent(0, true)
}

// Freq returns current, min and max frequency of the first CPU.
func Freq() CPUFreq {
	infos, err := cpu.Info()
	var f CPUFreq
	if err == nil && len(infos) > 0 {
		f.Max = infos[0].Mhz
	}
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq"); err == nil {
		var khz float64
		if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &khz); err == nil {
			f.Current = khz / 1000
		}
	}
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); err == nil {
		var khz float64
		if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &khz); err == nil {
			f.Max = khz / 1000
		}
	}
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_min_freq"); err == nil {
		var khz float64
		if _, err := fmt.Sscan(strings.TrimSpace(string(data)), &khz); err == nil {
			f.Min = khz / 1000
		}
	}
	if f.Current == 0 {
		f.Current = f.Max
	}
	return f
}

// LoadAverage returns the 1, 5 and 15 minute load, or nil where unsupported.
func LoadAverage() []float64 {
	avg, err := load.Avg()
	if err != nil {
		return nil
	}
	return []float64{avg.Load1, avg.Load5, avg.Load15}
}

// CPUCount is the logical CPU count, at least 1.
func CPUCount() int {
	if n, err := cpu.Counts(true); err == nil && n > 0 {
		return n
	}
	return max(1, runtime.NumCPU())
}

// Memory is virtual memory or swap, in bytes.
type Memory struct {
	Total     uint64  `json:"total"`
	Used      uint64  `json:"used"`
	Free      uint64  `json:"free"`
	Available uint64  `json:"available"`
	Percent   float64 `json:"percent"`
}

func VirtualMemory() (Memory, error) {
	v, err := mem.VirtualMemory()
	if err != nil {
		return Memory{}, err
	}
	return Memory{Total: v.Total, Used: v.Used, Free: v.Free, Available: v.Available, Percent: v.UsedPercent}, nil
}

func Swap() (Memory, error) {
	s, err := mem.SwapMemory()
	if err != nil {
		return Memory{}, err
	}
	return Memory{Total: s.Total, Used: s.Used, Free: s.Free, Percent: s.UsedPercent}, nil
}

// Partition is one mounted filesystem.
type Partition struct {
	Device     string  `json:"device"`
	Mountpoint string  `json:"mountpoint"`
	Fstype     string  `json:"fstype"`
	Opts       string  `json:"opts"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	Percent    float64 `json:"percent"`
	IsNVMe     bool    `json:"is_nvme"`
}

var excludedFstypes = map[string]bool{
	"squashfs": true, "tmpfs": true, "devtmpfs": true, "proc": true, "sysfs": true, "devpts": true,
	"cgroup": true, "cgroup2": true, "pstore": true, "bpf": true, "tracefs": true, "debugfs": true,
	"securityfs": true, "configfs": true, "fusectl": true, "hugetlbfs": true, "mqueue": true,
	"overlay": true, "rpc_pipefs": true, "systemd-1": true,
}

// ShouldMonitor filters snap mounts, loop devices, read-only and pseudo filesystems, and tiny volumes.
func (p Partition) ShouldMonitor() bool {
	opts := strings.Split(strings.ToLower(p.Opts), ",")
	readOnly := false
	for _, o := range opts {
		if o == "ro" {
			readOnly = true
		}
		if o == "rw" {
			readOnly = false
			break
		}
	}
	switch {
	case strings.Contains(strings.ToLower(p.Mountpoint), "/snap/"),
		strings.Contains(strings.ToLower(p.Device), "loop"),
		readOnly,
		excludedFstypes[strings.ToLower(p.Fstype)]:
		return false
	}
	return !(p.Total > 0 && p.Total < 100*1024*1024)
}

// Partitions lists mounted filesystems with usage, optionally only the monitorable ones.
func Partitions(onlyMonitorable bool) []Partition {
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []Partition
	for _, p := range parts {
		if seen[p.Mountpoint] {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		part := Partition{
			Device: p.Device, Mountpoint: p.Mountpoint, Fstype: p.Fstype, Opts: strings.Join(p.Opts, ","),
			Total: usage.Total, Used: usage.Used, Free: usage.Free, Percent: usage.UsedPercent,
			IsNVMe: runtime.GOOS == "linux" && strings.Contains(strings.ToLower(p.Device), "nvme"),
		}
		if onlyMonitorable && !part.ShouldMonitor() {
			continue
		}
		seen[p.Mountpoint] = true
		out = append(out, part)
	}
	return out
}

// DiskTotal sums usage across partitions.
type DiskTotal struct {
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	Percent    float64 `json:"percent"`
	Partitions int     `json:"partitions"`
}

func TotalDisk(parts []Partition) DiskTotal {
	t := DiskTotal{Partitions: len(parts)}
	for _, p := range parts {
		t.Total += p.Total
		t.Used += p.Used
		t.Free += p.Free
	}
	if t.Total > 0 {
		t.Percent = float64(t.Used) / float64(t.Total) * 100
	}
	return t
}

// DiskUsage returns usage for the filesystem holding path.
func DiskUsage(path string) (Partition, error) {
	u, err := disk.Usage(path)
	if err != nil {
		return Partition{}, err
	}
	return Partition{Mountpoint: path, Total: u.Total, Used: u.Used, Free: u.Free, Percent: u.UsedPercent}, nil
}

// IO is cumulative disk or network byte counters.
type IO struct {
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
	BytesSent  uint64 `json:"bytes_sent"`
	BytesRecv  uint64 `json:"bytes_recv"`
}

func DiskIO() (IO, error) {
	counters, err := disk.IOCounters()
	if err != nil {
		return IO{}, err
	}
	var io IO
	for _, c := range counters {
		io.ReadBytes += c.ReadBytes
		io.WriteBytes += c.WriteBytes
	}
	return io, nil
}

func NetworkIO() (IO, error) {
	counters, err := net.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return IO{}, err
	}
	return IO{BytesSent: counters[0].BytesSent, BytesRecv: counters[0].BytesRecv}, nil
}

// Bandwidth is the current transfer rate.
type Bandwidth struct {
	SentPerSec float64 `json:"bytes_sent_per_sec"`
	RecvPerSec float64 `json:"bytes_recv_per_sec"`
}

// BandwidthMeter computes rates from successive counter reads without blocking.
type BandwidthMeter struct {
	mu   sync.Mutex
	last IO
	at   time.Time
	rate Bandwidth
}

// Read returns the rate since the previous read. The first read primes the meter and returns false.
func (m *BandwidthMeter) Read() (Bandwidth, bool) {
	io, err := NetworkIO()
	if err != nil {
		return Bandwidth{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if m.at.IsZero() {
		m.last, m.at = io, now
		return Bandwidth{}, false
	}
	elapsed := now.Sub(m.at).Seconds()
	if elapsed < 0.2 {
		return m.rate, true
	}
	delta := func(cur, prev uint64) float64 {
		if cur < prev {
			return 0
		}
		return float64(cur - prev)
	}
	m.rate = Bandwidth{
		SentPerSec: delta(io.BytesSent, m.last.BytesSent) / elapsed,
		RecvPerSec: delta(io.BytesRecv, m.last.BytesRecv) / elapsed,
	}
	m.last, m.at = io, now
	return m.rate, true
}

// Interface is one network interface.
type Interface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"`
	IsUp      bool     `json:"is_up"`
	Speed     int      `json:"speed"`
}

// Interfaces lists non-loopback interfaces.
func Interfaces() []Interface {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []Interface
	for _, i := range ifaces {
		up, loopback := false, false
		for _, f := range i.Flags {
			up = up || f == "up"
			loopback = loopback || f == "loopback"
		}
		if loopback {
			continue
		}
		iface := Interface{Name: i.Name, IsUp: up}
		for _, a := range i.Addrs {
			iface.Addresses = append(iface.Addresses, a.Addr)
		}
		if data, err := os.ReadFile("/sys/class/net/" + i.Name + "/speed"); err == nil {
			fmt.Sscan(strings.TrimSpace(string(data)), &iface.Speed)
		}
		out = append(out, iface)
	}
	return out
}

// System is host identity and uptime.
type System struct {
	Hostname        string `json:"hostname"`
	Platform        string `json:"platform"`
	PlatformRelease string `json:"platform_release"`
	PlatformVersion string `json:"platform_version"`
	Architecture    string `json:"architecture"`
	BootTime        int64  `json:"boot_time"`
	UptimeSeconds   int64  `json:"uptime_seconds"`
	UptimeFormatted string `json:"uptime_formatted"`
	Users           int    `json:"users"`
}

func SystemInfo() System {
	s := System{Architecture: runtime.GOARCH, Platform: runtime.GOOS}
	if info, err := host.Info(); err == nil {
		s.Hostname = info.Hostname
		s.Platform = info.OS
		s.PlatformRelease = info.KernelVersion
		s.PlatformVersion = strings.TrimSpace(info.Platform + " " + info.PlatformVersion)
		s.BootTime = int64(info.BootTime)
		s.UptimeSeconds = int64(info.Uptime)
	}
	if s.Platform == "linux" {
		s.Platform = "Linux"
	}
	if users, err := host.Users(); err == nil {
		s.Users = len(users)
	}
	up := s.UptimeSeconds
	s.UptimeFormatted = fmt.Sprintf("%d days, %d hours, %d minutes, %d seconds", up/86400, up%86400/3600, up%3600/60, up%60)
	return s
}
