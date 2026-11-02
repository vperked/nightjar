package collect

import (
	"sort"
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
	handles  map[int32]*process.Process
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
	return &ProcessScanner{handles: map[int32]*process.Process{}, cpuCount: float64(CPUCount())}
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
	live := make(map[int32]*process.Process, len(pids))
	var all []Process
	for _, pid := range pids {
		h, ok := s.handles[pid]
		if !ok {
			if h, err = process.NewProcess(pid); err != nil {
				continue
			}
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
		if st, err := h.Status(); err == nil && len(st) > 0 {
			p.Status = st[0]
			overview.ByStatus[st[0]]++
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
