package collect

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// Process is one process as shown in alerts and the dashboard.
type Process struct {
	PID           int32    `json:"pid"`
	Name          string   `json:"name"`
	Status        string   `json:"status,omitempty"`
	Username      string   `json:"username,omitempty"`
	CPUPercent    float64  `json:"cpu_percent"`
	MemoryPercent float64  `json:"memory_percent"`
	RSS           uint64   `json:"rss,omitempty"`
	VMS           uint64   `json:"vms,omitempty"`
	Cmdline       []string `json:"cmdline,omitempty"`
}

// ProcessOverview is one shared scan: counts plus the top consumers.
type ProcessOverview struct {
	Total     int            `json:"total"`
	ByStatus  map[string]int `json:"by_status"`
	TopCPU    []Process      `json:"top_cpu"`
	TopMemory []Process      `json:"top_memory"`
}

// ProcessScanner keeps process handles between scans so CPU percent is measured over the interval,
// and shares one scan between the alert loop and the dashboard.
type ProcessScanner struct {
	mu       sync.Mutex
	handles  map[int32]*procHandle
	cpuCount float64
	cached   *ProcessOverview
	cachedAt time.Time
	limit    int
}

const sharedScanMaxAge = 2 * time.Second

func NewProcessScanner() *ProcessScanner {
	// Boot time only changes across a reboot, which restarts the monitor; without the cache every
	// process's CPU reading re-reads /proc/stat.
	process.EnableBootTimeCache(true)
	return &ProcessScanner{handles: map[int32]*procHandle{}, cpuCount: float64(CPUCount())}
}

// Overview returns the top `limit` processes by CPU and memory, reusing a scan younger than 2s.
func (s *ProcessScanner) Overview(limit int) ProcessOverview {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == nil || s.limit < limit || time.Since(s.cachedAt) >= sharedScanMaxAge {
		o := s.scan(limit)
		s.cached, s.cachedAt, s.limit = &o, time.Now(), limit
	}
	o := *s.cached
	if len(o.TopCPU) > limit {
		o.TopCPU = o.TopCPU[:limit]
	}
	if len(o.TopMemory) > limit {
		o.TopMemory = o.TopMemory[:limit]
	}
	return o
}

func (s *ProcessScanner) scan(limit int) ProcessOverview {
	pids, err := process.Pids()
	overview := ProcessOverview{ByStatus: map[string]int{}}
	if err != nil {
		return overview
	}
	// One /proc/meminfo read per scan; MemoryPercent would re-read it for every process.
	var memTotal float64
	if vm, err := VirtualMemory(); err == nil {
		memTotal = float64(vm.Total)
	}
	live := make(map[int32]*procHandle, len(pids))
	var all []Process
	for _, pid := range pids {
		h, status, ok := s.handle(pid)
		if !ok {
			continue
		}
		live[pid] = h
		name, err := h.Name()
		if err != nil {
			continue
		}
		p := Process{PID: pid, Name: name}
		if cpu, err := h.Percent(0); err == nil {
			p.CPUPercent = min(cpu/s.cpuCount, 100)
		}
		if m, err := h.MemoryInfo(); err == nil {
			p.RSS, p.VMS = m.RSS, m.VMS
			if memTotal > 0 {
				p.MemoryPercent = float64(m.RSS) / memTotal * 100
			}
		}
		if status != nil && *status != "" {
			p.Status = *status
			overview.ByStatus[*status]++
		}
		all = append(all, p)
	}
	s.handles = live
	overview.Total = len(all)
	if limit <= 0 || len(all) == 0 {
		return overview
	}

	top := func(less func(a, b Process) bool) []Process {
		sorted := append([]Process(nil), all...)
		sort.Slice(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })
		if len(sorted) > limit {
			sorted = sorted[:limit]
		}
		for i := range sorted {
			s.enrich(&sorted[i])
		}
		return sorted
	}
	overview.TopCPU = top(func(a, b Process) bool { return a.CPUPercent > b.CPUPercent })
	overview.TopMemory = top(func(a, b Process) bool { return a.MemoryPercent > b.MemoryPercent })
	return overview
}

// procHandle is a process's handle and the start time it was made for.
type procHandle struct {
	*process.Process
	start uint64
}

// handle returns the handle for pid and its status (nil when unknown); ok is false when the
// process is gone. A handle caches its process's name and CPU times, so a PID taken by a new
// process since the last scan gets a new one, or the newcomer is shown under the old name.
func (s *ProcessScanner) handle(pid int32) (h *procHandle, status *string, ok bool) {
	if runtime.GOOS != "linux" {
		h = s.handles[pid]
		if h != nil {
			if same, err := h.IsRunning(); err != nil || !same {
				h = nil
			}
		}
		if h == nil {
			p, err := process.NewProcess(pid)
			if err != nil {
				return nil, nil, false
			}
			h = &procHandle{Process: p}
		}
		if st, err := h.Status(); err == nil && len(st) > 0 {
			status = &st[0]
		}
		return h, status, true
	}
	// One read of /proc/<pid>/stat tells the process's state and when it started, in place of
	// gopsutil's IsRunning and Status, which read the stat and status files again for each.
	state, start, err := procStat(pid)
	if err != nil {
		return nil, nil, false
	}
	h = s.handles[pid]
	if h == nil || h.start != start {
		p, err := process.NewProcess(pid)
		if err != nil {
			return nil, nil, false
		}
		h = &procHandle{Process: p, start: start}
	}
	return h, &state, true
}

// procRoot is where /proc is, which gopsutil reads too: HOST_PROC when set, for a container
// watching its host.
var procRoot = cmp.Or(os.Getenv("HOST_PROC"), "/proc")

var errProcStat = errors.New("unreadable /proc/<pid>/stat")

// procStat reads a process's state, named as gopsutil names it ("running", "sleep"…), and its
// start time in clock ticks since boot, from /proc/<pid>/stat.
func procStat(pid int32) (state string, start uint64, err error) {
	data, err := os.ReadFile(procRoot + "/" + strconv.Itoa(int(pid)) + "/stat")
	if err != nil {
		return "", 0, err
	}
	// "pid (comm) S ppid …": the name may hold spaces and parentheses, so the fields start after
	// the last ")". The state is field 3, the start time field 22.
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return "", 0, errProcStat
	}
	f := bytes.Fields(data[i+1:])
	if len(f) < 20 || len(f[0]) == 0 {
		return "", 0, errProcStat
	}
	start, err = strconv.ParseUint(string(f[19]), 10, 64)
	if err != nil {
		return "", 0, errProcStat
	}
	return processStates[f[0][0]], start, nil
}

// processStates are gopsutil's names for the state letters; others are unknown ("").
var processStates = map[byte]string{
	'A': process.Daemon, 'D': process.Blocked, 'U': process.Blocked, 'E': process.Detached, 'I': process.Idle,
	'L': process.Lock, 'O': process.Orphan, 'R': process.Running, 'S': process.Sleep, 'T': process.Stop,
	't': process.Stop, 'W': process.Wait, 'Y': process.System, 'Z': process.Zombie,
}

func (s *ProcessScanner) enrich(p *Process) {
	h := s.handles[p.PID]
	if h == nil {
		return
	}
	if u, err := h.Username(); err == nil {
		p.Username = u
	}
	if c, err := h.CmdlineSlice(); err == nil {
		p.Cmdline = c
	}
}
