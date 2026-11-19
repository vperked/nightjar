// Package updates keeps software on the host up to date: OS packages, snaps, Debian packages,
// Sonarr-style apps (through their own updater), git checkouts including the monitor itself, and
// programs compared with their latest GitHub release. Each target is checked on an interval, and
// those set to update automatically are installed in a nightly window, restarted and checked to be running.
package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Kind is how a target is checked and updated.
type Kind string

const (
	KindSystem  Kind = "system"  // every OS package, with apt, dnf, yum or pacman
	KindSnap    Kind = "snap"    // every snap
	KindApt     Kind = "apt"     // one Debian package
	KindArr     Kind = "arr"     // Sonarr, Radarr, Prowlarr…: their built-in updater, through their API
	KindGit     Kind = "git"     // a git checkout that follows its upstream branch
	KindRelease Kind = "release" // a program compared with its project's latest GitHub release
)

var Kinds = []Kind{KindSystem, KindSnap, KindApt, KindArr, KindGit, KindRelease}

// Spec is one thing to keep updated. Only the fields of its kind matter.
type Spec struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Auto installs updates in the update window; otherwise they wait for the dashboard.
	Auto bool `json:"auto"`
	// Paused targets are not checked.
	Paused bool `json:"paused"`
	// Unit is the systemd service that must be running after an update; Restart restarts it first
	// (for git and release targets; the other kinds restart their own services).
	Unit    string `json:"unit,omitempty"`
	Restart bool   `json:"restart,omitempty"`

	Package string `json:"package,omitempty"` // apt

	URL        string `json:"url,omitempty"`         // arr: base URL; read from ConfigFile when empty
	APIKey     string `json:"api_key,omitempty"`     // arr: read from ConfigFile when empty
	ConfigFile string `json:"config_file,omitempty"` // arr: the app's config.xml

	// Dir is the checkout (git), or the folder a release's UpdateCommand works in (release): either
	// runs as the folder's owner, so what it builds or installs cannot act as root on their files.
	Dir string `json:"dir,omitempty"`

	Repo      string `json:"repo,omitempty"`       // release: GitHub owner/name
	TagPrefix string `json:"tag_prefix,omitempty"` // release: compare tags with this prefix instead of releases

	// Asset is a file of the latest release of AssetRepo (Repo when empty) that UpdateCommand
	// installs. Nightjar finds it and the SHA-256 GitHub lists for it, refuses to update when there
	// is none, and gives the command both as $ASSET_URL and $ASSET_SHA256 (release).
	Asset     string `json:"asset,omitempty"`
	AssetRepo string `json:"asset_repo,omitempty"`

	// VersionCommand prints the installed version (release). UpdateCommand installs the update
	// (release), or builds after pulling (git); it sees $VERSION and $TAG.
	VersionCommand string `json:"version_command,omitempty"`
	UpdateCommand  string `json:"update_command,omitempty"`

	// Self marks the monitor's own checkout: after updating, the monitor restarts itself.
	Self bool `json:"self,omitempty"`
}

var (
	idRE    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	repoRE  = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
	unitRE  = regexp.MustCompile(`^\w[\w@.:-]*$`)
	pkgRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	assetRE = regexp.MustCompile(`^[\w][\w.+-]{0,199}$`)

	ErrNotFound = errors.New("no update target with that id")
	ErrExists   = errors.New("an update target with that id already exists")
	ErrBusy     = errors.New("it is being updated; try again when that finishes")
)

// Slug turns a name into an id: "Plex Media Server" → "plex-media-server".
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if reservedIDs[s] {
		s += "-target"
	}
	return s
}

// reservedIDs name routes beside the targets' own (PUT …/updates/settings is the schedule), so a
// target cannot take them: its changes would go to the route instead and be lost.
var reservedIDs = map[string]bool{"settings": true}

// ErrReservedID is a new target given an id kept for a route.
var ErrReservedID = errors.New(`the id "settings" is kept for the update settings; give the target another`)

// ParseSpec reads and validates a target from JSON.
func ParseSpec(data []byte) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("invalid update target: %v", err)
	}
	return s, s.normalize()
}

func (s *Spec) normalize() error {
	for _, p := range []*string{&s.ID, &s.Name, &s.Unit, &s.Package, &s.URL, &s.APIKey, &s.ConfigFile, &s.Dir, &s.Repo, &s.TagPrefix, &s.VersionCommand, &s.UpdateCommand, &s.Asset, &s.AssetRepo} {
		*p = strings.TrimSpace(*p)
	}
	s.URL = strings.TrimRight(s.URL, "/")
	if s.ID == "" {
		s.ID = Slug(s.Name)
	}
	switch {
	case s.Name == "":
		return errors.New("name is required")
	case !idRE.MatchString(s.ID):
		return errors.New("id must be lowercase letters, digits and dashes")
	case s.Unit != "" && !unitRE.MatchString(s.Unit):
		return fmt.Errorf("%q is not a systemd unit name", s.Unit)
	}
	switch s.Kind {
	case KindSystem, KindSnap:
	case KindApt:
		if !pkgRE.MatchString(s.Package) {
			return errors.New("package must be a Debian package name")
		}
	case KindArr:
		if s.URL == "" && s.ConfigFile == "" {
			return errors.New("give the app's URL and API key, or its config.xml")
		}
	case KindGit:
		if s.Dir == "" {
			return errors.New("the checkout's directory is required")
		}
	case KindRelease:
		if !repoRE.MatchString(s.Repo) {
			return errors.New("repository must look like owner/name")
		}
		if s.VersionCommand == "" {
			return errors.New("a command that prints the installed version is required")
		}
		if s.Asset != "" && !assetRE.MatchString(s.Asset) {
			return errors.New("asset must be a release file's name")
		}
		if s.AssetRepo != "" && !repoRE.MatchString(s.AssetRepo) {
			return errors.New("asset repository must look like owner/name")
		}
	default:
		return fmt.Errorf("kind must be one of %v", Kinds)
	}
	return nil
}

// CanUpdate reports whether the target knows how to install its updates (otherwise it is only checked).
func (s Spec) CanUpdate() bool { return s.Kind != KindRelease || s.UpdateCommand != "" }

// Commands lists the fields that make the monitor run something or read files of the user's choosing.
func (s Spec) Commands() [4]string {
	return [4]string{s.VersionCommand, s.UpdateCommand, s.Dir, s.ConfigFile}
}

// Locked is what a dashboard without sign-in may not change, since any program on its machine could
// then reach its API: the commands and paths, and everything else that decides what runs as root or
// where a stored secret goes. What a release installs (Repo, Asset, AssetRepo), which service is
// restarted (Unit, Restart), where an *arr app's stored API key is sent (URL), and the monitor's own
// restart (Self).
type Locked struct {
	Commands         [4]string
	Repo, TagPrefix  string
	Asset, AssetRepo string
	Unit             string
	Restart, Self    bool
	URL              string
}

// KeepUnsent is s with what a save from the dashboard leaves out filled in from the stored old, as
// Save fills it: the download a release's recipe checks, while its command stays the same.
func (s Spec) KeepUnsent(old Spec) Spec {
	if s.Asset == "" && s.UpdateCommand == old.UpdateCommand {
		s.Asset, s.AssetRepo = old.Asset, old.AssetRepo
	}
	return s
}

// Locked is the spec's part that sign-in guards.
func (s Spec) Locked() Locked {
	return Locked{Commands: s.Commands(), Repo: s.Repo, TagPrefix: s.TagPrefix, Asset: s.Asset, AssetRepo: s.AssetRepo, Unit: s.Unit, Restart: s.Restart, Self: s.Self, URL: s.URL}
}
