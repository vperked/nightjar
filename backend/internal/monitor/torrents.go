package monitor

import (
	"cmp"
	"context"
	"fmt"
	"nightjar/internal/arr"
	"sort"
	"strings"
	"time"

	"nightjar/internal/discord"
	"nightjar/internal/qbittorrent"
)

// torrentWatcher tracks torrents across cycles to recover stalls, apply the cleanup policy and notify.
type torrentWatcher struct {
	handler
	counter int
	tracked map[string]*trackedTorrent
}

type trackedTorrent struct {
	state, name                                        string
	notifiedDownload, notifiedSeeding, notifiedStalled bool
	stalledSince, unstalledSince                       *float64
	lastStalledAction                                  *float64
	recoveryAttempts                                   int
	replaced                                           bool // handed to Sonarr or Radarr to replace
	lastRatioAction                                    float64
	seenAt                                             float64
}

const (
	qbCheckInterval    = 2
	maxTrackedTorrents = 1000
)

func info(t qbittorrent.Torrent) discord.TorrentInfo {
	return discord.TorrentInfo{
		Name: t.Name, State: t.State, SavePath: t.SavePath, Size: t.Size, Uploaded: t.Uploaded,
		ProgressPercent: t.ProgressPercent(), Ratio: t.Ratio, DLSpeed: t.DLSpeed, UPSpeed: t.UPSpeed,
		Seeds: t.Seeds, Peers: t.Peers, ETA: t.ETA,
	}
}

func fptr(v float64) *float64 { return &v }

func (h *torrentWatcher) check(s *Snapshot) {
	qb := h.ctx.QBittorrent
	if qb == nil {
		return
	}
	h.counter++
	if h.counter < qbCheckInterval {
		return
	}
	h.counter = 0
	board := h.ctx.QBittorrentBoard
	fail := func(fallback string) {
		if board != nil {
			board.Publish(s.Now, qb.URL(), qb.Version(), cmp.Or(qb.LastError(), fallback), nil, nil)
		}
	}
	if !qb.Available() {
		fail("qBittorrent is not answering")
		return
	}
	// A nil list means the fetch failed (or timed out); an empty one, that there are no torrents.
	torrents := withTimeout(15*time.Second, []qbittorrent.Torrent(nil), "qBittorrent torrent list", func() []qbittorrent.Torrent {
		t, _ := qb.List()
		return t
	})
	if torrents == nil {
		// An empty list from a failed fetch must not be mistaken for "every torrent is gone".
		h.log.Debug("qBittorrent torrent list unavailable")
		fail("could not list torrents")
		return
	}
	live := map[string]bool{}
	for _, t := range torrents {
		if t.Hash == "" {
			continue
		}
		live[t.Hash] = true
		h.process(t, s.Now)
	}
	for hash := range h.tracked {
		if !live[hash] {
			delete(h.tracked, hash)
		}
	}
	if over := len(h.tracked) - maxTrackedTorrents; over > 0 {
		hashes := make([]string, 0, len(h.tracked))
		for k := range h.tracked {
			hashes = append(hashes, k)
		}
		sort.Slice(hashes, func(i, j int) bool { return h.tracked[hashes[i]].seenAt < h.tracked[hashes[j]].seenAt })
		for _, k := range hashes[:over] {
			delete(h.tracked, k)
		}
	}
	if board != nil {
		h.publish(board, qb, torrents, s.Now)
	}
	h.ctx.Heartbeat()
}

// publish hands the dashboard this fetch, with each torrent's stall time and recovery attempts.
func (h *torrentWatcher) publish(board *qbittorrent.Board, qb *qbittorrent.Client, torrents []qbittorrent.Torrent, now float64) {
	entries := make([]qbittorrent.Entry, 0, len(torrents))
	for _, t := range torrents {
		e := qbittorrent.Entry{Torrent: t}
		if tr := h.tracked[t.Hash]; tr != nil {
			if tr.stalledSince != nil && tr.unstalledSince == nil {
				e.StalledFor = now - *tr.stalledSince
			}
			e.Recoveries = tr.recoveryAttempts
		}
		entries = append(entries, e)
	}
	transfer := withTimeout(10*time.Second, (*qbittorrent.Transfer)(nil), "qBittorrent transfer info", func() *qbittorrent.Transfer {
		if t, err := qb.Transfer(); err == nil {
			return &t
		}
		return nil
	})
	board.Publish(now, qb.URL(), qb.Version(), "", entries, transfer)
}

func (h *torrentWatcher) process(t qbittorrent.Torrent, now float64) {
	cfg := h.ctx.Cfg
	stalled := qbittorrent.IsDownloadStalled(t)
	tr, ok := h.tracked[t.Hash]
	if !ok {
		tr = &trackedTorrent{state: t.State, name: t.Name, seenAt: now}
		if stalled {
			tr.stalledSince = fptr(now)
		}
		h.tracked[t.Hash] = tr
	}
	prev := tr.state
	if stalled {
		h.handleStall(t, tr, now)
	} else if tr.stalledSince != nil {
		if tr.unstalledSince == nil {
			tr.unstalledSince = fptr(now)
		} else if now-*tr.unstalledSince >= cfg.StalledRecoveryGrace {
			tr.stalledSince, tr.unstalledSince, tr.lastStalledAction = nil, nil, nil
			tr.recoveryAttempts, tr.notifiedStalled = 0, false
		}
	}
	reason := qbittorrent.RemoveReason(t, cfg.SeedingRatioLimit)
	if reason == "inactive" && !cfg.InactiveTorrentAutoRemove {
		reason = ""
	}
	if reason != "" {
		h.applyCleanup(t, tr, reason, now)
	}
	h.notifyProgress(t, tr, prev)
	tr.state = t.State
}

func (h *torrentWatcher) handleStall(t qbittorrent.Torrent, tr *trackedTorrent, now float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	tr.unstalledSince = nil
	if tr.stalledSince == nil {
		tr.stalledSince = fptr(now)
	}
	duration := now - *tr.stalledSince
	if tr.notifiedStalled && tr.recoveryAttempts >= cfg.StalledMaxRecoveryAttempts {
		// Recovery did not help: once more time has passed, Sonarr or Radarr replace it.
		if !tr.replaced && cfg.StalledReplace && tr.lastStalledAction != nil && now-*tr.lastStalledAction >= cfg.StalledActionCooldown {
			tr.replaced = true
			go h.replace(t, duration)
		}
		return
	}
	if duration < cfg.StalledThreshold || (tr.lastStalledAction != nil && now-*tr.lastStalledAction < cfg.StalledActionCooldown) {
		return
	}
	recover := tr.recoveryAttempts < cfg.StalledMaxRecoveryAttempts
	var actions []string
	recoveryOK := true
	if recover && cfg.StalledAutoHandle {
		actions = withTimeout(15*time.Second, []string(nil), "recover stalled torrent", func() []string { return ctx.QBittorrent.Recover(t.Hash) })
		recoveryOK = len(actions) > 0
		tr.recoveryAttempts++
		if recoveryOK {
			h.log.Warn(fmt.Sprintf("[qBittorrent] Recovering stalled torrent: %s (stalled for %.0fs; actions: %s)", t.Name, duration, strings.Join(actions, ", ")))
		} else {
			h.log.Warn(fmt.Sprintf("[qBittorrent] Failed to recover stalled torrent: %s (stalled for %.0fs)", t.Name, duration))
		}
		if tr.recoveryAttempts >= cfg.StalledMaxRecoveryAttempts {
			h.log.Info(fmt.Sprintf("[qBittorrent] Stopping stalled recovery for %s after %d attempt(s); torrent is still stalled", t.Name, tr.recoveryAttempts))
		}
	} else if recover {
		tr.recoveryAttempts = cfg.StalledMaxRecoveryAttempts
		h.log.Warn(fmt.Sprintf("[qBittorrent] Torrent stalled: %s (stalled for %.0fs)", t.Name, duration))
	}
	if !tr.notifiedStalled {
		tr.notifiedStalled = true
		if ctx.QBittorrentWebhook != nil {
			ok := discord.StalledTorrent(ctx.QBittorrentWebhook, info(t), duration, actions)
			ctx.Metrics.RecordAlert("torrent_stalled")
			ctx.Metrics.RecordWebhookResult(ok)
			tr.notifiedStalled = ok
		}
	}
	if recoveryOK {
		tr.lastStalledAction = fptr(now)
	} else {
		tr.lastStalledAction = fptr(now - cfg.StalledActionCooldown + cfg.StalledRetryBackoff)
	}
}

func (h *torrentWatcher) applyCleanup(t qbittorrent.Torrent, tr *trackedTorrent, reason string, now float64) {
	cfg, ctx := h.ctx.Cfg, h.ctx
	if now-tr.lastRatioAction < cfg.SeedingRatioActionCooldown {
		return
	}
	// Paused downloads may be partial; never delete their files.
	deleteFiles := cfg.SeedingRatioDeleteFiles && reason == "ratio"
	actions := withTimeout(15*time.Second, []string(nil), "torrent cleanup", func() []string { return ctx.QBittorrent.Delete(t.Hash, deleteFiles) })
	if reason == "inactive" {
		if len(actions) > 0 {
			h.log.Info(fmt.Sprintf("[qBittorrent] Removing inactive torrent: %s (state=%s; actions: %s)", t.Name, t.State, strings.Join(actions, ", ")))
		} else {
			h.log.Warn(fmt.Sprintf("[qBittorrent] Failed to remove inactive torrent: %s (state=%s)", t.Name, t.State))
		}
	} else {
		limit := fmt.Sprintf("ratio %.2f >= %.2f", t.Ratio, cfg.SeedingRatioLimit)
		if len(actions) > 0 {
			h.log.Info(fmt.Sprintf("[qBittorrent] Ratio limit reached: %s (%s; actions: %s)", t.Name, limit, strings.Join(actions, ", ")))
		} else {
			h.log.Warn(fmt.Sprintf("[qBittorrent] Failed to remove ratio-complete torrent: %s (%s)", t.Name, limit))
		}
	}
	if wh := ctx.QBittorrentWebhook; wh != nil {
		var ok bool
		if reason == "inactive" {
			ok = discord.InactiveTorrent(wh, info(t), actions)
			ctx.Metrics.RecordAlert("torrent_inactive")
		} else {
			ok = discord.RatioLimit(wh, info(t), cfg.SeedingRatioLimit, actions)
			ctx.Metrics.RecordAlert("torrent_ratio_limit")
		}
		ctx.Metrics.RecordWebhookResult(ok)
	}
	if len(actions) > 0 {
		tr.lastRatioAction = now
	} else {
		tr.lastRatioAction = now - cfg.SeedingRatioActionCooldown + cfg.SeedingRatioRetryBackoff
	}
}

func (h *torrentWatcher) notifyProgress(t qbittorrent.Torrent, tr *trackedTorrent, prev string) {
	wh := h.ctx.QBittorrentWebhook
	seeding := qbittorrent.SeedingActiveStates[t.State]
	if seeding && t.Progress >= 1 && !tr.notifiedDownload && qbittorrent.DownloadingStates[prev] {
		h.log.Info("[qBittorrent] Download complete: " + t.Name)
		if wh != nil {
			discord.DownloadComplete(wh, info(t))
		}
		tr.notifiedDownload = true
	}
	if seeding && !qbittorrent.SeedingActiveStates[prev] && !tr.notifiedSeeding {
		h.log.Info("[qBittorrent] Torrent seeding: " + t.Name)
		if wh != nil {
			discord.Seeding(wh, info(t))
		}
		tr.notifiedSeeding = true
	}
}

// replace has whichever of Sonarr and Radarr queued a stalled download remove it, blocklist the
// release and search for another, and says so.
func (h *torrentWatcher) replace(t qbittorrent.Torrent, stalled float64) {
	cfg := h.ctx.Config()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, c := range []*arr.Client{arr.Connect("sonarr", cfg.SonarrURL, cfg.SonarrAPIKey), arr.Connect("radarr", cfg.RadarrURL, cfg.RadarrAPIKey)} {
		if c == nil {
			continue
		}
		q, ok, err := c.Queued(ctx, t.Hash)
		if err != nil {
			h.log.Warn(fmt.Sprintf("[qBittorrent] Could not ask %s about %s: %v", c.Name(), t.Name, err))
			continue
		}
		if !ok {
			continue
		}
		if err := c.Replace(ctx, q.ID); err != nil {
			h.log.Warn(fmt.Sprintf("[qBittorrent] %s could not replace %s: %v", c.Name(), t.Name, err))
			return
		}
		h.log.Info(fmt.Sprintf("[qBittorrent] %s replaced stalled download %s", c.Name(), t.Name))
		if h.ctx.QBittorrentWebhook != nil {
			h.ctx.QBittorrentWebhook.SendEmbed("Replaced: "+discord.Truncate(t.Name, 200),
				fmt.Sprintf("It stalled for %s and recovery did not help, so %s removed it, blocklisted the release and is searching for another.", discord.Span(stalled), c.Name()),
				discord.Info, []discord.Field{{Name: "Progress", Value: fmt.Sprintf("%.0f%%", t.ProgressPercent()), Inline: true}}, "qBittorrent")
		}
		return
	}
}
