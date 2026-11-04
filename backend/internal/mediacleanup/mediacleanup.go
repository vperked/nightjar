// Package mediacleanup removes movies and shows that nobody watches. Something no one on the
// Plex server has ever played, a set time after Plex added it, goes on a Leaving soon list and
// Discord is told. A week later (unless it was marked Keep, or someone started watching it) it is
// removed: its torrents first, once they have seeded to the ratio (or after giving up waiting),
// then the media itself, through Sonarr or Radarr so they do not fetch it again, or through Plex
// for what they do not manage.
package mediacleanup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"nightjar/internal/arr"
	"nightjar/internal/config"
	"nightjar/internal/discord"
	"nightjar/internal/format"
	"nightjar/internal/logx"
	"nightjar/internal/plex"
	"nightjar/internal/qbittorrent"
	"nightjar/internal/storage"
)

var log = logx.Component("Cleanup")

// Status values of an item.
const (
	Preview = "preview" // it qualifies, but the cleanup is off
	Leaving = "leaving" // listed, to go at LeavingAt
	Waiting = "waiting" // due, but a torrent is still seeding to the ratio
	Removed = "removed"
	Failed  = "failed"
)

// maxRemovals caps what one pass removes, so a mistake cannot empty a library at once.
const maxRemovals = 10

// Torrent is a torrent an item came from.
type Torrent struct {
	Hash  string  `json:"hash"`
	Name  string  `json:"name"`
	Ratio float64 `json:"ratio"`
	Size  int64   `json:"size"`
}

// Item is one movie or show on the list.
type Item struct {
	Key       string    `json:"key"` // Plex's ratingKey
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Year      int       `json:"year,omitempty"`
	Library   string    `json:"library"`
	Added     float64   `json:"added"`
	Size      int64     `json:"size"`
	Status    string    `json:"status"`
	Listed    float64   `json:"listed"`
	LeavingAt float64   `json:"leaving_at"`
	Reason    string    `json:"reason,omitempty"`
	Manager   string    `json:"manager"` // radarr, sonarr or plex
	Torrents  []Torrent `json:"torrents"`
	RemovedAt float64   `json:"removed_at,omitempty"`

	guids []string
	files []string
	path  string // where its files are, to see how full that disk is
}

// Kept is an item the owner chose to keep, by its title for the list.
type Kept struct {
	Key   string  `json:"key"`
	Title string  `json:"title"`
	At    float64 `json:"at"`
}

type state struct {
	Items    map[string]*Item `json:"items"`
	Kept     map[string]Kept  `json:"kept"`
	Scanned  float64          `json:"scanned"`
	Error    string           `json:"error,omitempty"`
	Freed    int64            `json:"freed"` // bytes freed in all
	Notified map[string]bool  `json:"notified"`
}

const stateKey = "mediacleanup.state"

// Sources is what a pass works with; any of them may be nil.
type Sources struct {
	Plex   *plex.Client
	QB     *qbittorrent.Client
	Sonarr *arr.Client
	Radarr *arr.Client
}

// Manager runs the cleanup. Its methods are safe for concurrent use.
type Manager struct {
	Store  *storage.Store
	Config func() config.Settings
	Send   discord.Sender
	// Connect builds the sources for a pass from the settings in force.
	Connect func(config.Settings) Sources
	Now     func() time.Time
	// Record, when set, keeps each removal in the activity log.
	Record func(action, target string)

	mu      sync.Mutex
	running sync.Mutex
}

func (m *Manager) load() state {
	st := state{Items: map[string]*Item{}, Kept: map[string]Kept{}, Notified: map[string]bool{}}
	if raw := m.Store.Meta(stateKey); raw != "" {
		json.Unmarshal([]byte(raw), &st)
	}
	if st.Items == nil {
		st.Items = map[string]*Item{}
	}
	if st.Kept == nil {
		st.Kept = map[string]Kept{}
	}
	if st.Notified == nil {
		st.Notified = map[string]bool{}
	}
	return st
}

func (m *Manager) save(st state) {
	data, _ := json.Marshal(st)
	m.Store.SetMeta(stateKey, string(data))
}

// View is the list for the dashboard.
type View struct {
	Enabled bool    `json:"enabled"`
	Items   []Item  `json:"items"`
	Kept    []Kept  `json:"kept"`
	Scanned float64 `json:"scanned"`
	Error   string  `json:"error,omitempty"`
	Freed   int64   `json:"freed"`
	// The rules in force, to say them on the page.
	Days       int      `json:"days"`
	NoticeDays int      `json:"notice_days"`
	Ratio      float64  `json:"ratio"`
	RatioDays  int      `json:"ratio_days"`
	Managers   []string `json:"managers"` // the apps it removes through: Sonarr, Radarr
	Found      []string `json:"found"`    // the ones found on this machine rather than set
}

// View is the current list, soonest to go first, then what was removed lately.
func (m *Manager) View() View {
	m.mu.Lock()
	st := m.load()
	m.mu.Unlock()
	cfg := m.Config()
	v := View{Enabled: cfg.MediaCleanup, Items: []Item{}, Kept: []Kept{}, Scanned: st.Scanned, Error: st.Error, Freed: st.Freed,
		Days: cfg.MediaCleanupDays, NoticeDays: cfg.MediaCleanupNoticeDays, Ratio: cfg.MediaCleanupRatio, RatioDays: cfg.MediaCleanupRatioDays, Managers: []string{}, Found: []string{}}
	// Set in the settings, or found on this machine.
	for _, a := range []struct{ kind, name, url, key string }{{"sonarr", "Sonarr", cfg.SonarrURL, cfg.SonarrAPIKey}, {"radarr", "Radarr", cfg.RadarrURL, cfg.RadarrAPIKey}} {
		if a.url != "" && a.key != "" {
			v.Managers = append(v.Managers, a.name)
		} else if f, ok := arr.Discover(a.kind); ok {
			v.Managers = append(v.Managers, a.name)
			v.Found = append(v.Found, a.name+" at "+f.URL+" (from "+f.From+")")
		}
	}
	for _, it := range st.Items {
		v.Items = append(v.Items, *it)
	}
	order := map[string]int{Waiting: 0, Leaving: 1, Preview: 2, Failed: 3, Removed: 4}
	slices.SortFunc(v.Items, func(a, b Item) int {
		return cmp.Or(order[a.Status]-order[b.Status], cmp.Compare(a.LeavingAt, b.LeavingAt), cmp.Compare(b.RemovedAt, a.RemovedAt), strings.Compare(a.Title, b.Title))
	})
	for _, k := range st.Kept {
		v.Kept = append(v.Kept, k)
	}
	slices.SortFunc(v.Kept, func(a, b Kept) int { return strings.Compare(a.Title, b.Title) })
	return v
}

// ErrUnknown means the item is not on the list.
var ErrUnknown = errors.New("that is not on the cleanup list")

// Keep spares an item from now on (or, with keep false, lets it be listed again).
func (m *Manager) Keep(key string, keep bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.load()
	if keep {
		it, ok := st.Items[key]
		if !ok {
			return ErrUnknown
		}
		if it.Status == Removed {
			return errors.New("it was already removed")
		}
		st.Kept[key] = Kept{Key: key, Title: it.Title, At: float64(m.Now().Unix())}
		delete(st.Items, key)
	} else {
		if _, ok := st.Kept[key]; !ok {
			return ErrUnknown
		}
		delete(st.Kept, key)
	}
	m.save(st)
	return nil
}

// Scan looks at the libraries, updates the list, and (with the cleanup on) removes what is due.
func (m *Manager) Scan(ctx context.Context) error {
	if !m.running.TryLock() {
		return errors.New("a cleanup pass is already running")
	}
	defer m.running.Unlock()
	cfg := m.Config()
	src := m.Connect(cfg)
	now := m.Now()
	t := float64(now.Unix())
	err := m.scan(ctx, cfg, src, t)
	m.mu.Lock()
	st := m.load()
	st.Scanned = t
	st.Error = ""
	if err != nil {
		st.Error = err.Error()
		log.Warn("Media cleanup pass failed", "err", err)
	}
	m.save(st)
	m.mu.Unlock()
	return err
}

func (m *Manager) scan(ctx context.Context, cfg config.Settings, src Sources, t float64) error {
	if src.Plex == nil {
		return errors.New("Plex is not connected")
	}
	// Without the whole watch history Nightjar cannot be sure what nobody watched: it stops.
	watched, err := src.Plex.EverWatched()
	if err != nil {
		return fmt.Errorf("could not read Plex's watch history, so nothing was listed: %v", err)
	}
	played := map[string]bool{}
	for _, p := range m.Store.PlexPlays(0) {
		played[strings.ToLower(p.Title)] = true
	}
	libs, err := src.Plex.Sections()
	if err != nil {
		return fmt.Errorf("could not list Plex's libraries: %v", err)
	}
	var found []plex.Item
	for _, lib := range libs {
		if (lib.Type != "movie" && lib.Type != "show") || (len(cfg.MediaCleanupLibraries) > 0 && !slices.ContainsFunc(cfg.MediaCleanupLibraries, func(n string) bool { return strings.EqualFold(n, lib.Title) })) {
			continue
		}
		items, err := src.Plex.Items(lib)
		if err != nil {
			return fmt.Errorf("could not list %s: %v", lib.Title, err)
		}
		found = append(found, items...)
	}

	m.mu.Lock()
	st := m.load()
	m.mu.Unlock()
	cutoff := t - float64(cfg.MediaCleanupDays)*86400
	qualifies := map[string]plex.Item{}
	for _, it := range found {
		if it.Watched || watched[it.Key] || played[strings.ToLower(it.Title)] || it.Added == 0 || it.Added > cutoff {
			continue
		}
		if _, kept := st.Kept[it.Key]; kept {
			continue
		}
		qualifies[it.Key] = it
	}

	// The list: new arrivals, arrivals turned on, and what no longer qualifies (watched since,
	// or gone from Plex) dropped. What was removed stays a month, for the record.
	var newly []*Item
	for key, it := range st.Items {
		if _, still := qualifies[key]; !still && it.Status != Removed {
			delete(st.Items, key)
		}
		if it.Status == Removed && t-it.RemovedAt > 30*86400 {
			delete(st.Items, key)
		}
	}
	notice := float64(cfg.MediaCleanupNoticeDays) * 86400
	for key, p := range qualifies {
		it, ok := st.Items[key]
		if !ok {
			it = &Item{Key: key, Kind: p.Kind, Title: p.Title, Year: p.Year, Library: p.Library, Added: p.Added, Size: p.Size, Status: Preview, Listed: t, Torrents: []Torrent{}}
			st.Items[key] = it
		}
		it.guids, it.files = p.Guids, p.Files
		if len(p.Files) > 0 {
			it.path = p.Files[0]
		}
		if cfg.MediaCleanup && it.Status == Preview {
			it.Status, it.Listed, it.LeavingAt = Leaving, t, t+notice
			newly = append(newly, it)
		}
		if !cfg.MediaCleanup && (it.Status == Leaving || it.Status == Waiting) {
			it.Status, it.LeavingAt, it.Reason = Preview, 0, ""
		}
	}

	// Who removes each, and the torrents it came from.
	var entries = map[string][]arr.Entry{}
	for _, c := range []*arr.Client{src.Sonarr, src.Radarr} {
		if c != nil {
			list, err := c.List(ctx)
			if err != nil {
				return err
			}
			entries[c.Kind] = list
		}
	}
	var torrents []qbittorrent.Torrent
	if src.QB != nil {
		if torrents, err = src.QB.List(); err != nil {
			return fmt.Errorf("could not list qBittorrent's torrents: %v", err)
		}
	}
	for _, it := range st.Items {
		if it.Status == Removed {
			continue
		}
		it.Manager, it.Torrents = "plex", []Torrent{}
		c := src.Radarr
		if it.Kind == "show" {
			c = src.Sonarr
		}
		var hashes []string
		if c != nil {
			if e, ok := arr.Match(entries[c.Kind], it.guids, it.files); ok {
				it.Manager = c.Kind
				if it.path == "" {
					it.path = e.Path
				}
				if e.Size > 0 && it.Size == 0 {
					it.Size = e.Size
				}
				hashes, _ = c.Downloads(ctx, e.ID)
			}
		}
		for _, tr := range torrents {
			if slices.Contains(hashes, strings.ToLower(tr.Hash)) || (len(hashes) == 0 && sameTitle(tr.Name, it.Title, it.Year)) {
				it.Torrents = append(it.Torrents, Torrent{Hash: tr.Hash, Name: tr.Name, Ratio: tr.Ratio, Size: tr.Size})
			}
		}
	}

	// A disk nearly full hurries what is leaving from it: a day's notice instead of the week.
	if cfg.MediaCleanup && cfg.MediaCleanupDiskFull > 0 {
		for _, it := range st.Items {
			if it.Status != Leaving || it.path == "" {
				continue
			}
			if used, ok := DiskUsed(it.path); ok && used >= float64(cfg.MediaCleanupDiskFull) {
				if soon := it.Listed + 86400; soon < it.LeavingAt {
					it.LeavingAt = soon
				}
				it.Reason = fmt.Sprintf("the disk it is on is %.0f%% full, so it leaves sooner", used)
			}
		}
	}

	// What is due goes, a few at a time.
	var gone []*Item
	if cfg.MediaCleanup {
		giveUp := float64(cfg.MediaCleanupRatioDays) * 86400
		for _, it := range sortedDue(st.Items, t) {
			if len(gone) >= maxRemovals {
				break
			}
			low := slices.IndexFunc(it.Torrents, func(tr Torrent) bool { return tr.Ratio < cfg.MediaCleanupRatio })
			if low >= 0 && t < it.LeavingAt+giveUp {
				tr := it.Torrents[low]
				it.Status = Waiting
				it.Reason = fmt.Sprintf("seeding to ratio %.2f: at %.2f; removed anyway on %s", cfg.MediaCleanupRatio, tr.Ratio, time.Unix(int64(it.LeavingAt+giveUp), 0).Format("Mon 2 Jan"))
				continue
			}
			if err := m.remove(ctx, src, it, entries); err != nil {
				it.Status, it.Reason = Failed, err.Error()
				log.Warn("Could not remove "+it.Title, "err", err)
				continue
			}
			it.Status, it.Reason, it.RemovedAt = Removed, "", t
			st.Freed += it.Size
			gone = append(gone, it)
			log.Info("Removed " + it.Title + " (never watched)")
			if m.Record != nil {
				m.Record("Removed "+describe(it)+" (never watched), through "+it.Manager, it.Title)
			}
		}
	}

	m.mu.Lock()
	// Keep and unkeep may have happened meanwhile: theirs wins.
	latest := m.load()
	for key := range latest.Kept {
		delete(st.Items, key)
	}
	st.Kept = latest.Kept
	m.save(st)
	m.mu.Unlock()
	m.announce(newly, gone, cfg)
	return nil
}

// sortedDue is what should go by t, soonest first.
func sortedDue(items map[string]*Item, t float64) []*Item {
	var due []*Item
	for _, it := range items {
		if (it.Status == Leaving || it.Status == Waiting || it.Status == Failed) && it.LeavingAt > 0 && it.LeavingAt <= t {
			due = append(due, it)
		}
	}
	// Biggest first when the disk is hurrying them, else soonest first.
	slices.SortFunc(due, func(a, b *Item) int {
		if strings.Contains(a.Reason, "full") && strings.Contains(b.Reason, "full") {
			return cmp.Compare(b.Size, a.Size)
		}
		return cmp.Compare(a.LeavingAt, b.LeavingAt)
	})
	return due
}

// remove deletes the torrents with their files, then the media through whoever manages it.
func (m *Manager) remove(ctx context.Context, src Sources, it *Item, entries map[string][]arr.Entry) error {
	for _, tr := range it.Torrents {
		if src.QB == nil || src.QB.Delete(tr.Hash, true) == nil {
			return fmt.Errorf("qBittorrent did not remove the torrent %s", tr.Name)
		}
	}
	switch it.Manager {
	case "sonarr", "radarr":
		c := src.Radarr
		if it.Manager == "sonarr" {
			c = src.Sonarr
		}
		e, ok := arr.Match(entries[it.Manager], it.guids, it.files)
		if !ok {
			return fmt.Errorf("%s no longer has it", c.Name())
		}
		return c.Delete(ctx, e.ID)
	}
	return src.Plex.DeleteItem(it.Key)
}

var wordRE = regexp.MustCompile(`[^a-z0-9]+`)

// sameTitle reports whether a torrent's name is plainly the item: its title as a whole run of
// words, and for a movie its year too. Short titles never match by name alone.
func sameTitle(name, title string, year int) bool {
	n := " " + strings.TrimSpace(wordRE.ReplaceAllString(strings.ToLower(name), " ")) + " "
	t := strings.TrimSpace(wordRE.ReplaceAllString(strings.ToLower(title), " "))
	if len(t) < 4 || !strings.Contains(n, " "+t+" ") {
		return false
	}
	return year == 0 || strings.Contains(n, fmt.Sprintf(" %d ", year))
}

func describe(it *Item) string {
	s := it.Title
	if it.Year > 0 {
		s += fmt.Sprintf(" (%d)", it.Year)
	}
	if it.Size > 0 {
		s += " · " + format.Bytes(float64(it.Size))
	}
	return s
}

// announce tells Discord what was listed and what was removed in this pass.
func (m *Manager) announce(newly, gone []*Item, cfg config.Settings) {
	if m.Send == nil {
		return
	}
	lines := func(list []*Item) string {
		var out []string
		for i, it := range list {
			if i == 12 {
				out = append(out, fmt.Sprintf("and %d more", len(list)-12))
				break
			}
			out = append(out, describe(it))
		}
		return discord.List(out)
	}
	if len(newly) > 0 {
		keys := make([]string, 0, len(newly))
		for _, it := range newly[:min(5, len(newly))] {
			keys = append(keys, it.Key)
		}
		send := discord.About(m.Send, "cleanup:"+strings.Join(keys, ","))
		when := time.Unix(int64(newly[0].LeavingAt), 0).Format("Mon 2 Jan")
		desc := fmt.Sprintf("No one has watched these in the %d days since they were added. They will be removed on %s unless someone starts one, or you mark it Keep on the dashboard's Cleanup page.", cfg.MediaCleanupDays, when)
		send.SendEmbed(fmt.Sprintf("Leaving Plex on %s: %d %s", when, len(newly), noun(len(newly))), desc, discord.Info, []discord.Field{{Name: "Leaving", Value: lines(newly)}}, "Plex")
	}
	if len(gone) > 0 {
		var freed int64
		for _, it := range gone {
			freed += it.Size
		}
		desc := "Never watched, so they were removed with their files"
		if freed > 0 {
			desc += ", freeing " + format.Bytes(float64(freed))
		}
		m.Send.SendEmbed(fmt.Sprintf("Removed from Plex: %d %s", len(gone), noun(len(gone))), desc+".", discord.Info, []discord.Field{{Name: "Removed", Value: lines(gone)}}, "Plex")
	}
}

func noun(n int) string {
	if n == 1 {
		return "title"
	}
	return "titles"
}

// Loop runs a pass an hour after starting and every six hours after, until stop closes.
func (m *Manager) Loop(stop <-chan struct{}) {
	wait := time.Hour
	for {
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
		wait = 6 * time.Hour
		if m.Config().PlexMonitor {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			m.Scan(ctx)
			cancel()
		}
	}
}

// DiskUsed is how full the disk holding path is, in percent; tests replace it. A path that does
// not exist (yet) is measured at the nearest folder above it that does.
var DiskUsed = func(path string) (float64, bool) {
	for p := path; p != "" && p != "."; p = filepath.Dir(p) {
		var fs syscall.Statfs_t
		if syscall.Statfs(p, &fs) == nil {
			total := float64(fs.Blocks) * float64(fs.Bsize)
			if total == 0 {
				return 0, false
			}
			return (1 - float64(fs.Bavail)*float64(fs.Bsize)/total) * 100, true
		}
		if p == "/" {
			break
		}
	}
	return 0, false
}
