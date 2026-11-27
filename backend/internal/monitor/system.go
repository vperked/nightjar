package monitor

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/format"
	"nightjar/internal/smart"
	"nightjar/internal/units"
)

// ---------- systemd services ----------

// serviceWatcher polls systemd in the background and alerts when a service fails, keeps
// restarting, or comes back.
type serviceWatcher struct {
	handler
	lastPoll float64
	running  atomic.Bool
	wg       sync.WaitGroup
}

func (h *serviceWatcher) check(s *Snapshot) {
	cfg, m := h.ctx.Cfg, h.ctx.Units
	if m == nil || !cfg.ServiceWatchEnabled || s.Now-h.lastPoll < cfg.ServiceWatchInterval || !h.running.CompareAndSwap(false, true) {
		return
	}
	h.lastPoll = s.Now
	ignore, flapAt := slices.Clone(cfg.ServiceWatchIgnore), cfg.ServiceRestartAlert
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer h.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		events, err := m.Poll(ctx, time.Now(), ignore, flapAt)
		if err != nil {
			h.log.Warn("Could not read systemd services", "err", err)
			return
		}
		for _, e := range events {
			h.alert(ctx, m, e)
		}
	}()
}

func (h *serviceWatcher) alert(ctx context.Context, m *units.Monitor, e units.Event) {
	u := e.Unit
	name := strings.TrimSuffix(u.Name, ".service")
	hook := discord.About(h.ctx.Webhook, "unit:"+u.Name)
	// logs reads the journal once, works out the likely cause from it, and returns the fields that
	// say so: the cause and its fix first, then the last few lines as evidence.
	logs := func() []discord.Field {
		lines, err := m.Logs(ctx, u.Name, 80)
		if err != nil || len(lines) == 0 {
			return nil
		}
		var fields []discord.Field
		if found := m.Diagnose(u.Name, lines); len(found) > 0 {
			f := found[0]
			m.SetCause(u.Name, f)
			h.log.Warn("Likely cause", "unit", u.Name, "cause", f.Title)
			fields = append(fields, discord.Field{Name: "Likely cause", Value: clipField(f.Title + "\n" + f.Explain + "\n\n**Fix:** " + f.Fix)})
		}
		text := strings.Join(lines[max(0, len(lines)-12):], "\n")
		if len(text) > 950 {
			text = "…" + text[len(text)-950:]
		}
		return append(fields, discord.Field{Name: "Last log lines", Value: "```\n" + text + "\n```"})
	}
	switch e.Kind {
	case units.EventFailed:
		why := cmp.Or(u.Result, u.Sub)
		if u.ExitStatus != 0 {
			why += fmt.Sprintf(", exit status %d", u.ExitStatus)
		}
		h.log.Error("Service failed", "unit", u.Name, "result", why)
		if hook != nil {
			fields := append([]discord.Field{field("State", u.Active+" ("+why+")", true), field("Restarts", fmt.Sprint(u.Restarts), true)}, logs()...)
			hook.SendEmbed(name+" failed", cmp.Or(u.Description, u.Name)+" stopped and did not come back. Restart it from the dashboard's Services page.", discord.Critical, fields, "Services")
		}
	case units.EventRecovered:
		h.log.Info("Service running again", "unit", u.Name, "down", e.Downtime.Round(time.Second))
		if hook != nil {
			hook.SendEmbed(name+" is running again", cmp.Or(u.Description, u.Name), discord.OK, []discord.Field{field("Down for", discord.Span(e.Downtime.Seconds()), true)}, "Services")
		}
	case units.EventFlapping:
		h.log.Warn("Service keeps restarting", "unit", u.Name, "restarts", e.Restarts)
		if hook != nil {
			hook.SendEmbed(name+" keeps restarting", fmt.Sprintf("%s restarted %d times in the last ten minutes.", cmp.Or(u.Description, u.Name), e.Restarts), discord.Warning, logs(), "Services")
		}
	}
}

// clipField keeps a Discord field under its 1024-character limit.
func clipField(s string) string {
	if len(s) > 1000 {
		return s[:1000] + "…"
	}
	return s
}

// ---------- drive health ----------

const drivesKey = "smart.drives"

// driveWatcher reads SMART on its own interval in the background. Each problem is announced once:
// a baseline per drive, kept in the database, remembers what was already said.
type driveWatcher struct {
	handler
	lastRead float64
	lastErr  string
	running  atomic.Bool
	wg       sync.WaitGroup
	// baselines and silent are what the database holds of each drive since it was first read here,
	// so a database that is unavailable, or a read of it that fails, does not announce the same
	// problem again at every read. Only the reading goroutine, one at a time, uses them.
	baselines map[string]baseline
	silent    map[string]bool
}

func (h *driveWatcher) check(s *Snapshot) {
	cfg, r := h.ctx.Cfg, h.ctx.Drives
	if r == nil || !cfg.SmartMonitorEnabled || s.Now-h.lastRead < cfg.SmartCheckInterval || !h.running.CompareAndSwap(false, true) {
		return
	}
	h.lastRead = s.Now
	limits := smart.Limits{HDD: cfg.SmartHDDTempLimit, SSD: cfg.SmartSSDTempLimit}
	every := cfg.SmartCheckInterval
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		defer h.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		now := time.Now()
		drives, err := r.Read(ctx, now, limits)
		if err != nil {
			if err.Error() != h.lastErr {
				h.log.Warn("Could not read drive health", "err", err)
			}
			h.lastErr = err.Error()
			return
		}
		h.lastErr = ""
		for _, d := range drives {
			switch {
			case d.Error != "":
				// It was read before and stopped answering, which is how a dying disk often looks.
				if !d.Asleep && d.Checked != 0 {
					h.unanswered(d)
				}
			case d.Checked == float64(now.Unix()):
				h.compare(d, s.Now, every)
				h.answering(d)
			}
		}
		if data, err := json.Marshal(drives); err == nil {
			h.ctx.Store.SetMeta(drivesKey, string(data))
		}
	}()
}

// baseline is what has already been announced about a drive.
type baseline struct {
	Failing                                         bool
	Reallocated, Pending, Uncorrectable, Media, CRC int64
	WearStep                                        int // wear in steps of 25% once past 100%
	SpareLow                                        bool
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func baselineOf(d smart.Drive) baseline {
	b := baseline{Failing: d.Health == "failing", Reallocated: val(d.Reallocated), Pending: val(d.Pending), Uncorrectable: val(d.Uncorrectable), Media: val(d.MediaErrors), CRC: val(d.CRCErrors)}
	if d.PercentUsed != nil && *d.PercentUsed >= 100 {
		b.WearStep = *d.PercentUsed / 25
	}
	b.SpareLow = d.AvailableSpare != nil && d.SpareThreshold != nil && *d.AvailableSpare <= *d.SpareThreshold+10
	return b
}

// driveDevice is where a drive is read. Drives behind a RAID controller share one device
// (/dev/bus/0) and differ by their slot in smartctl's type (megaraid,0 and megaraid,1).
func driveDevice(d smart.Drive) string {
	if strings.Contains(d.Type, ",") {
		return d.Device + " " + d.Type
	}
	return d.Device
}

func driveName(d smart.Drive) string {
	if d.Model != "" {
		return d.Model + " (" + driveDevice(d) + ")"
	}
	return driveDevice(d)
}

// driveID names a drive across reboots, which can reorder device names.
func driveID(d smart.Drive) string { return cmp.Or(d.Serial, driveDevice(d)) }

// unansweredKey marks a drive whose stopping answering was already announced, until it reads again.
func unansweredKey(d smart.Drive) string { return "smart.unanswered." + driveID(d) }

// unanswered announces, once per outage, a drive that was read before and now cannot be.
func (h *driveWatcher) unanswered(d smart.Drive) {
	key := unansweredKey(d)
	if h.silent[key] || h.ctx.Store.Meta(key) != "" {
		return
	}
	if h.silent == nil {
		h.silent = map[string]bool{}
	}
	h.silent[key] = true
	h.ctx.Store.SetMeta(key, d.Error)
	h.log.Warn("Drive stopped answering", "drive", driveName(d), "err", d.Error)
	if hook := discord.About(h.ctx.Webhook, "drive:"+driveID(d)); hook != nil {
		hook.SendEmbed("Drive not answering: "+driveName(d), "SMART could not read it: "+d.Error+"\nA drive that stops answering is often failing, or its cable is loose: check it, and back up what is on it.",
			discord.Warning, nil, "Disk")
	}
}

// answering clears a drive's outage once it reads again, so the next one is announced too.
func (h *driveWatcher) answering(d smart.Drive) {
	if key := unansweredKey(d); h.silent[key] || h.ctx.Store.Meta(key) != "" {
		delete(h.silent, key)
		h.log.Info("Drive answering again", "drive", driveName(d))
		h.ctx.Store.SetMeta(key, "")
	}
}

// compare announces what is new about a drive read every interval seconds.
func (h *driveWatcher) compare(d smart.Drive, now, interval float64) {
	key := "smart.baseline." + driveID(d)
	old, known := h.baselines[key]
	if !known {
		json.Unmarshal([]byte(h.ctx.Store.Meta(key)), &old)
	}
	cur := baselineOf(d)

	var news []string
	more := func(now, before int64, what string) {
		if now > before {
			news = append(news, fmt.Sprintf("%d %s (+%d)", now, what, now-before))
		}
	}
	more(cur.Reallocated, old.Reallocated, "reallocated sectors")
	more(cur.Pending, old.Pending, "sectors waiting to be reallocated")
	more(cur.Uncorrectable, old.Uncorrectable, "uncorrectable errors")
	more(cur.Media, old.Media, "media errors")
	more(cur.CRC, old.CRC, "cable or connection (CRC) errors")
	if cur.WearStep > old.WearStep {
		news = append(news, fmt.Sprintf("%d%% of its rated write endurance used: plan to replace it", *d.PercentUsed))
	}
	if cur.SpareLow && !old.SpareLow {
		news = append(news, fmt.Sprintf("spare capacity down to %d%%", *d.AvailableSpare))
	}

	hook := discord.About(h.ctx.Webhook, "drive:"+driveID(d))
	fields := []discord.Field{}
	if d.Temperature != nil {
		fields = append(fields, field("Temperature", fmt.Sprintf("%d°C", *d.Temperature), true))
	}
	if d.PowerOnHours != nil {
		fields = append(fields, field("Powered on", format.Duration(float64(*d.PowerOnHours)*3600), true))
	}
	switch {
	case cur.Failing && !old.Failing:
		h.log.Error("Drive failing", "drive", driveName(d), "problems", d.Problems)
		if hook != nil {
			hook.SendEmbed("Drive failing: "+driveName(d), "Back up what is on it and replace it.\n• "+strings.Join(d.Problems, "\n• "), discord.Critical, fields, "Disk")
		}
	case len(news) > 0:
		h.log.Warn("Drive health warning", "drive", driveName(d), "news", news)
		if hook != nil {
			hook.SendEmbed("Drive health warning: "+driveName(d), "• "+strings.Join(news, "\n• "), discord.Warning, fields, "Disk")
		}
	}
	// Heat comes and goes, so it is gated like the other threshold alerts rather than announced once.
	if d.Temperature != nil && slices.ContainsFunc(d.Problems, func(p string) bool { return strings.HasPrefix(p, "running hot") }) {
		msg := fmt.Sprintf("%s is at %d°C", driveName(d), *d.Temperature)
		// Drives are read every interval, so the episode must outlast the gap between reads.
		h.raiseAlertEvery("drive-temp:"+driveDevice(d), now, interval, msg, hook, func() bool {
			return hook.SendEmbed("Drive running hot: "+driveName(d), msg, discord.Warning, fields, "Disk")
		}, "drive_temperature", false)
	}
	if h.baselines == nil {
		h.baselines = map[string]baseline{}
	}
	h.baselines[key] = cur
	if data, err := json.Marshal(cur); err == nil {
		h.ctx.Store.SetMeta(key, string(data))
	}
}

// LoadDrives restores the last readings, so a drive asleep since the last start still shows.
func LoadDrives(ctx *Context) {
	var drives []smart.Drive
	if json.Unmarshal([]byte(ctx.Store.Meta(drivesKey)), &drives) == nil && ctx.Drives != nil {
		ctx.Drives.Restore(drives)
	}
}
