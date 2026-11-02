// Package smart reads drive health with smartctl: the overall SMART verdict, temperature, and the
// counters that rise before a disk dies (reallocated, pending and uncorrectable sectors; NVMe media
// errors, wear and spare capacity). Sleeping disks are not woken: their last reading is kept.
package smart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// Drive is one disk's latest reading and the verdict drawn from it.
type Drive struct {
	Device   string `json:"device"`
	Type     string `json:"type"` // smartctl's -d type: sat, nvme…
	Model    string `json:"model,omitempty"`
	Serial   string `json:"serial,omitempty"`
	Capacity int64  `json:"capacity,omitempty"`
	// Rotational is true for spinning disks (their temperature limit is lower).
	Rotational bool `json:"rotational"`

	Passed       *bool `json:"passed,omitempty"`
	Temperature  *int  `json:"temperature,omitempty"`
	PowerOnHours *int  `json:"power_on_hours,omitempty"`

	// ATA counters (nil when the drive does not report them).
	Reallocated   *int64 `json:"reallocated,omitempty"`
	Pending       *int64 `json:"pending,omitempty"`
	Uncorrectable *int64 `json:"uncorrectable,omitempty"`
	CRCErrors     *int64 `json:"crc_errors,omitempty"`
	// FailingAttributes are ATA attributes at or below their failure threshold now.
	FailingAttributes []string `json:"failing_attributes,omitempty"`

	// NVMe health log.
	CriticalWarning *int   `json:"critical_warning,omitempty"`
	PercentUsed     *int   `json:"percent_used,omitempty"`
	AvailableSpare  *int   `json:"available_spare,omitempty"`
	SpareThreshold  *int   `json:"spare_threshold,omitempty"`
	MediaErrors     *int64 `json:"media_errors,omitempty"`
	UnsafeShutdowns *int64 `json:"unsafe_shutdowns,omitempty"`

	Asleep  bool    `json:"asleep"`            // in standby at the last check, so not read
	Checked float64 `json:"checked,omitempty"` // when the reading below was taken
	Error   string  `json:"error,omitempty"`

	Health   string   `json:"health"` // good, warning, failing or unknown
	Problems []string `json:"problems,omitempty"`
}

// Limits are the temperatures above which a drive runs too hot.
type Limits struct{ HDD, SSD int }

// Runner runs smartctl; tests replace it.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

func execRunner(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "smartctl", args...).Output()
	// smartctl's exit status is a bit mask that also flags warnings; its JSON says what happened.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(out) > 0 {
		return out, nil
	}
	return out, err
}

// Reader keeps the last reading of each drive.
type Reader struct {
	Run Runner

	mu     sync.Mutex
	drives map[string]Drive
	order  []string
	err    string
}

func NewReader() *Reader { return &Reader{Run: execRunner, drives: map[string]Drive{}} }

// ErrNoSmartctl means smartmontools is not installed.
var ErrNoSmartctl = errors.New("smartctl is not installed (apt install smartmontools)")

// Read scans for drives and reads each one that is awake. A sleeping drive keeps its previous reading.
func (r *Reader) Read(ctx context.Context, now time.Time, limits Limits) ([]Drive, error) {
	out, err := r.Run(ctx, "--scan", "--json")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			err = ErrNoSmartctl
		}
		r.setErr(err)
		return nil, err
	}
	var scan struct {
		Devices []struct{ Name, Type string }
	}
	if err := json.Unmarshal(out, &scan); err != nil {
		r.setErr(err)
		return nil, err
	}
	var drives []Drive
	for _, dev := range scan.Devices {
		d := r.readOne(ctx, dev.Name, dev.Type, now)
		Assess(&d, limits)
		drives = append(drives, d)
	}
	r.mu.Lock()
	r.err = ""
	r.order = r.order[:0]
	for _, d := range drives {
		r.drives[d.Device] = d
		r.order = append(r.order, d.Device)
	}
	r.mu.Unlock()
	return drives, nil
}

func (r *Reader) setErr(err error) {
	r.mu.Lock()
	r.err = err.Error()
	r.mu.Unlock()
}

func (r *Reader) readOne(ctx context.Context, device, typ string, now time.Time) Drive {
	r.mu.Lock()
	prev, known := r.drives[device]
	r.mu.Unlock()
	// -n standby: a sleeping disk answers "in standby" instead of spinning up.
	out, err := r.Run(ctx, "--all", "--json", "--nocheck=standby", "--device="+typ, device)
	if err != nil {
		prev.Device, prev.Type, prev.Error = device, typ, err.Error()
		return prev
	}
	var raw report
	if err := json.Unmarshal(out, &raw); err != nil {
		prev.Device, prev.Type, prev.Error = device, typ, "unreadable smartctl output: "+err.Error()
		return prev
	}
	if raw.asleep() {
		if !known {
			prev = Drive{Device: device, Type: typ}
		}
		prev.Asleep, prev.Error = true, ""
		return prev
	}
	d := raw.drive(device, typ)
	d.Checked = float64(now.Unix())
	if d.Model == "" && d.Passed == nil {
		d.Error = raw.message()
	}
	return d
}

// Restore seeds the last readings (from before a restart), shown until the drives are read again.
func (r *Reader) Restore(drives []Drive) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range drives {
		if _, ok := r.drives[d.Device]; !ok {
			r.drives[d.Device] = d
			r.order = append(r.order, d.Device)
		}
	}
}

// Drives returns the last reading of every drive, and why the last scan failed if it did.
func (r *Reader) Drives() ([]Drive, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Drive, 0, len(r.order))
	for _, dev := range r.order {
		out = append(out, r.drives[dev])
	}
	return out, r.err
}

// Assess draws the verdict from a reading: failing when the drive itself says so, warning on the
// counters that precede failure, wear past its rating, or heat.
func Assess(d *Drive, limits Limits) {
	d.Problems = nil
	failing := false
	add := func(fail bool, format string, args ...any) {
		d.Problems = append(d.Problems, fmt.Sprintf(format, args...))
		failing = failing || fail
	}
	if d.Passed != nil && !*d.Passed {
		add(true, "SMART overall health check failed")
	}
	for _, a := range d.FailingAttributes {
		add(true, "attribute %s is past its failure threshold", a)
	}
	if v := d.CriticalWarning; v != nil && *v != 0 {
		add(*v&^0x02 != 0, "NVMe critical warning: %s", criticalWarning(*v))
	}
	count := func(v *int64, what string) {
		if v != nil && *v > 0 {
			add(false, "%d %s", *v, what)
		}
	}
	count(d.Reallocated, "reallocated sectors")
	count(d.Pending, "sectors waiting to be reallocated")
	count(d.Uncorrectable, "uncorrectable errors")
	count(d.MediaErrors, "media errors")
	if v := d.CRCErrors; v != nil && *v > 0 {
		add(false, "%d cable or connection (CRC) errors", *v)
	}
	if v := d.PercentUsed; v != nil && *v >= 100 {
		add(false, "%d%% of its rated write endurance used", *v)
	}
	if d.AvailableSpare != nil && d.SpareThreshold != nil && *d.AvailableSpare <= *d.SpareThreshold+10 {
		add(false, "spare capacity down to %d%% (fails at %d%%)", *d.AvailableSpare, *d.SpareThreshold)
	}
	if limit := limits.SSD; d.Temperature != nil {
		if d.Rotational {
			limit = limits.HDD
		}
		if limit > 0 && *d.Temperature >= limit {
			add(false, "running hot: %d°C (limit %d°C)", *d.Temperature, limit)
		}
	}
	switch {
	case failing:
		d.Health = "failing"
	case len(d.Problems) > 0:
		d.Health = "warning"
	case d.Passed == nil && d.CriticalWarning == nil:
		d.Health = "unknown"
	default:
		d.Health = "good"
	}
}

func criticalWarning(v int) string {
	var parts []string
	for bit, what := range map[int]string{0x01: "spare capacity below threshold", 0x02: "temperature out of range", 0x04: "reliability degraded", 0x08: "read-only", 0x10: "volatile backup failed"} {
		if v&bit != 0 {
			parts = append(parts, what)
		}
	}
	slices.Sort(parts)
	if len(parts) == 0 {
		return fmt.Sprintf("0x%x", v)
	}
	return fmt.Sprint(parts)
}

// ---------- smartctl JSON ----------

type report struct {
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
		Messages   []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	UserCapacity struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	RotationRate *int `json:"rotation_rate"`
	SmartStatus  *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	ATA *struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	NVMe *struct {
		CriticalWarning int   `json:"critical_warning"`
		AvailableSpare  int   `json:"available_spare"`
		SpareThreshold  int   `json:"available_spare_threshold"`
		PercentageUsed  int   `json:"percentage_used"`
		MediaErrors     int64 `json:"media_errors"`
		UnsafeShutdowns int64 `json:"unsafe_shutdowns"`
	} `json:"nvme_smart_health_information_log"`
}

// standbyBit is smartctl's exit-status bit for "device is in a low-power mode and was not checked".
const standbyBit = 1 << 1

func (r report) asleep() bool {
	for _, m := range r.Smartctl.Messages {
		if msg := strings.ToLower(m.String); strings.Contains(msg, "standby") || strings.Contains(msg, "sleep") {
			return true
		}
	}
	return r.ModelName == "" && r.Smartctl.ExitStatus&standbyBit != 0 && r.SmartStatus == nil
}

func (r report) message() string {
	if len(r.Smartctl.Messages) > 0 {
		return r.Smartctl.Messages[0].String
	}
	return "smartctl returned no health data"
}

func (r report) drive(device, typ string) Drive {
	d := Drive{Device: device, Type: typ, Model: r.ModelName, Serial: r.SerialNumber, Capacity: r.UserCapacity.Bytes}
	d.Rotational = r.RotationRate != nil && *r.RotationRate > 0
	if r.SmartStatus != nil {
		d.Passed = ptr(r.SmartStatus.Passed)
	}
	if r.Temperature != nil {
		d.Temperature = ptr(r.Temperature.Current)
	}
	if r.PowerOnTime != nil {
		d.PowerOnHours = ptr(r.PowerOnTime.Hours)
	}
	if r.ATA != nil {
		for _, a := range r.ATA.Table {
			v := a.Raw.Value
			switch a.ID {
			case 5:
				d.Reallocated = ptr(v)
			case 197:
				d.Pending = ptr(v)
			case 187, 198:
				if d.Uncorrectable == nil || v > *d.Uncorrectable {
					d.Uncorrectable = ptr(v)
				}
			case 199:
				d.CRCErrors = ptr(v)
			}
			if a.WhenFailed == "now" {
				d.FailingAttributes = append(d.FailingAttributes, a.Name)
			}
		}
	}
	if n := r.NVMe; n != nil {
		d.CriticalWarning, d.PercentUsed = ptr(n.CriticalWarning), ptr(n.PercentageUsed)
		d.AvailableSpare, d.SpareThreshold = ptr(n.AvailableSpare), ptr(n.SpareThreshold)
		d.MediaErrors, d.UnsafeShutdowns = ptr(n.MediaErrors), ptr(n.UnsafeShutdowns)
	}
	return d
}

func ptr[T any](v T) *T { return &v }
