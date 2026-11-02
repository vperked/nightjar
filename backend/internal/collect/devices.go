package collect

import (
	"context"
	"encoding/csv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GPU is one NVIDIA GPU as reported by nvidia-smi.
type GPU struct {
	Name           string  `json:"name"`
	DriverVersion  string  `json:"driver_version"`
	Utilization    float64 `json:"utilization"`
	MemUtilization float64 `json:"memory_utilization"`
	MemTotalMB     float64 `json:"memory_total_mb"`
	MemUsedMB      float64 `json:"memory_used_mb"`
	MemPercent     float64 `json:"memory_percent"`
	Temperature    float64 `json:"temperature"`
	PowerDraw      float64 `json:"power_draw"`
	PowerLimit     float64 `json:"power_limit"`
}

// GPUReader queries nvidia-smi when it is installed.
type GPUReader struct {
	once      sync.Once
	available bool
}

func (g *GPUReader) Available() bool {
	g.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		g.available = exec.CommandContext(ctx, "nvidia-smi", "--version").Run() == nil
	})
	return g.available
}

const gpuQuery = "name,driver_version,utilization.gpu,utilization.memory,memory.total,memory.used,temperature.gpu,power.draw,power.limit"

// Read returns the first GPU, or nil when none is available.
func (g *GPUReader) Read() *GPU {
	if !g.Available() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu="+gpuQuery, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimSpace(string(out)))).ReadAll()
	if err != nil || len(rows) == 0 || len(rows[0]) < 9 {
		return nil
	}
	r := rows[0]
	f := func(i int) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(r[i]), 64)
		return v
	}
	gpu := &GPU{
		Name: strings.TrimSpace(r[0]), DriverVersion: strings.TrimSpace(r[1]),
		Utilization: f(2), MemUtilization: f(3), MemTotalMB: f(4), MemUsedMB: f(5),
		Temperature: f(6), PowerDraw: f(7), PowerLimit: f(8),
	}
	if gpu.MemTotalMB > 0 {
		gpu.MemPercent = gpu.MemUsedMB / gpu.MemTotalMB * 100
	}
	return gpu
}

// Fan is one hwmon fan reading.
type Fan struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	RPM   int    `json:"speed_rpm"`
}

type thermalZone struct {
	kind, tempFile string
}

// Sensors reads thermal zones and hwmon fans from sysfs.
type Sensors struct {
	zones   []thermalZone
	fanDirs [2][]string
}

// hwmon drivers whose fan files are known to hang or misreport.
var fanBlacklist = map[string]bool{"nouveau": true, "amdgpu": true, "radeon": true, "nvidia": true}

func NewSensors() *Sensors {
	s := &Sensors{}
	dirs, _ := filepath.Glob("/sys/class/thermal/thermal_zone*")
	for _, d := range dirs {
		kind, err := os.ReadFile(filepath.Join(d, "type"))
		if err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(d, "temp")); err == nil {
			s.zones = append(s.zones, thermalZone{kind: strings.TrimSpace(string(kind)), tempFile: filepath.Join(d, "temp")})
		}
	}
	hwmons, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	sort.Strings(hwmons)
	for _, h := range hwmons {
		name, err := os.ReadFile(filepath.Join(h, "name"))
		if err != nil || fanBlacklist[strings.ToLower(strings.TrimSpace(string(name)))] {
			continue
		}
		s.fanDirs[0] = append(s.fanDirs[0], h)
		if _, err := os.Stat(filepath.Join(h, "device")); err == nil {
			s.fanDirs[1] = append(s.fanDirs[1], filepath.Join(h, "device"))
		}
	}
	return s
}

func (s *Sensors) Available() bool { return len(s.zones) > 0 }

// CPUTemperature prefers zones named cpu/processor/core and falls back to the hottest zone.
func (s *Sensors) CPUTemperature() *float64 {
	var cpuTemp, maxTemp *float64
	for _, z := range s.zones {
		data, err := os.ReadFile(z.tempFile)
		if err != nil {
			continue
		}
		milli, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		t := float64(milli) / 1000
		if maxTemp == nil || t > *maxTemp {
			maxTemp = &t
		}
		kind := strings.ToLower(z.kind)
		if strings.Contains(kind, "cpu") || strings.Contains(kind, "processor") || strings.Contains(kind, "core") || strings.Contains(kind, "x86_pkg") {
			if cpuTemp == nil || t > *cpuTemp {
				cpuTemp = &t
			}
		}
	}
	if cpuTemp != nil {
		return cpuTemp
	}
	return maxTemp
}

// Fans reads hwmon fan inputs, falling back to device directories when hwmon has none.
func (s *Sensors) Fans() []Fan {
	for _, dirs := range s.fanDirs {
		var fans []Fan
		for _, d := range dirs {
			files, _ := filepath.Glob(filepath.Join(d, "fan*_input"))
			sort.Strings(files)
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				rpm, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil {
					continue
				}
				name := strings.TrimSuffix(filepath.Base(f), "_input")
				label := name
				if l, err := os.ReadFile(strings.TrimSuffix(f, "_input") + "_label"); err == nil {
					label = strings.TrimSpace(string(l))
				}
				fans = append(fans, Fan{Name: name, Label: label, RPM: rpm})
			}
		}
		if len(fans) > 0 {
			return fans
		}
	}
	return nil
}
