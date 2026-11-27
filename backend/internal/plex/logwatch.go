package plex

// Plex can be up and answering while playback fails: a file it cannot reach, a crash it restarts
// from, a damaged database. Its API says none of that, but its own log and crash-report folder
// do. LogWatcher reads both, from where they stood when it started, so old trouble is not
// reported again.

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

const (
	serverLog = "Plex Media Server.log"
	crashDir  = "Crash Reports"
	// maxFailureSamples caps the paths and lines one poll reports.
	maxFailureSamples = 5
	// maxRead caps what one poll reads, so a log that grew by gigabytes is not read in one go.
	maxRead = 8 << 20
)

var (
	missingFileRE = regexp.MustCompile(` ERROR - .*Couldn't find the file to stream: (.+)$`)
	// SQLite's own words for a damaged file, as Plex logs them.
	databaseRE = regexp.MustCompile(`(?i) ERROR - .*(database disk image is malformed|SQLITE_CORRUPT|file is not a database)`)
)

// Crash is a crash dump Plex wrote.
type Crash struct {
	Process string // "PLEX MEDIA SERVER", "PLEX MEDIA SCANNER", ...
	Version string
	Path    string
}

// Failures is what went wrong since the last poll. Samples are capped; the counts are not.
type Failures struct {
	MissingFiles []string // distinct paths Plex could not open to stream
	MissingCount int      // failed attempts, which a player's retries multiply
	Database     []string // the log lines that report a damaged database
	Crashes      []Crash
}

func (f Failures) Empty() bool {
	return f.MissingCount == 0 && len(f.Database) == 0 && len(f.Crashes) == 0
}

// LogWatcher reads Plex's log and crash reports under a Plex data folder.
type LogWatcher struct {
	dir string

	mu      sync.Mutex
	file    *os.File
	pending []byte // a line Plex has not finished writing
	started bool
	dumps   map[string]bool
}

// NewLogWatcher watches the Plex data folder dir: Logs and Crash Reports are under it.
func NewLogWatcher(dir string) *LogWatcher {
	return &LogWatcher{dir: dir}
}

// Poll returns the failures logged since the last poll. The first poll only marks where things
// stand.
func (w *LogWatcher) Poll() Failures {
	w.mu.Lock()
	defer w.mu.Unlock()
	var f Failures
	w.readLog(&f)
	w.scanCrashes(&f)
	w.started = true
	return f
}

// Close lets go of the log.
func (w *LogWatcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// readLog reads what was added to the log. Plex rotates it by renaming it to .1.log and starting
// a new one; the open file is the renamed one by then, so it is read to its end before the new
// one is opened.
func (w *LogWatcher) readLog(f *Failures) {
	path := filepath.Join(w.dir, "Logs", serverLog)
	if w.file == nil {
		file, err := os.Open(path)
		if err != nil {
			return
		}
		w.file, w.pending = file, nil
		if !w.started {
			file.Seek(0, io.SeekEnd)
			return
		}
	}
	if !w.drain(f) {
		return // more to read next time, before looking for a rotation
	}
	cur, err1 := w.file.Stat()
	now, err2 := os.Stat(path)
	if err1 != nil || err2 != nil || !os.SameFile(cur, now) {
		w.file.Close()
		w.file = nil
		if err2 == nil {
			w.readLog(f)
		}
		return
	}
	// Truncated in place: start again from its top.
	if pos, err := w.file.Seek(0, io.SeekCurrent); err == nil && now.Size() < pos {
		w.file.Seek(0, io.SeekStart)
		w.pending = nil
		w.drain(f)
	}
}

// drain reads up to maxRead bytes and says whether it reached the end of the file.
func (w *LogWatcher) drain(f *Failures) bool {
	lr := &io.LimitedReader{R: w.file, N: maxRead}
	r := bufio.NewReaderSize(lr, 64<<10)
	for {
		// The slice is the reader's own buffer, valid until the next read: only the rare line
		// that matters is copied.
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if chunk[len(chunk)-1] != '\n' {
				w.pending = append(w.pending, chunk...)
			} else {
				line := chunk
				if len(w.pending) > 0 {
					line = append(w.pending, chunk...)
					w.pending = nil
				}
				if line = bytes.TrimRight(line, "\r\n"); bytes.Contains(line, errorMark) {
					classify(string(line), f)
				}
			}
		}
		if err != nil && err != bufio.ErrBufferFull {
			return lr.N > 0
		}
	}
}

// errorMark is in every line classify looks at.
var errorMark = []byte(" ERROR - ")

func classify(line string, f *Failures) {
	if !strings.Contains(line, " ERROR - ") {
		return
	}
	if m := missingFileRE.FindStringSubmatch(line); m != nil {
		f.MissingCount++
		path := strings.TrimSpace(m[1])
		for _, p := range f.MissingFiles {
			if p == path {
				return
			}
		}
		if len(f.MissingFiles) < maxFailureSamples {
			f.MissingFiles = append(f.MissingFiles, path)
		}
		return
	}
	if databaseRE.MatchString(line) && len(f.Database) < maxFailureSamples {
		if i := strings.Index(line, " ERROR - "); i >= 0 {
			line = line[i+len(" ERROR - "):]
		}
		f.Database = append(f.Database, line)
	}
}

// scanCrashes finds crash dumps written since the first poll: Crash Reports/<version>/<process>/<id>.dmp.
func (w *LogWatcher) scanCrashes(f *Failures) {
	root := filepath.Join(w.dir, crashDir)
	seen := map[string]bool{}
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".dmp" {
			return nil
		}
		seen[path] = true
		if w.started && !w.dumps[path] {
			c := Crash{Path: path}
			if rel, err := filepath.Rel(root, path); err == nil {
				if parts := strings.Split(rel, string(filepath.Separator)); len(parts) == 3 {
					c.Version, c.Process = parts[0], parts[1]
				}
			}
			f.Crashes = append(f.Crashes, c)
		}
		return nil
	})
	w.dumps = seen
}

// FolderState says whether the folder holding path can be read now: "" when it can, otherwise
// why not ("missing", "unreadable"), which points at a drive or mount rather than one file.
func FolderState(path string) string {
	_, err := os.Stat(filepath.Dir(path))
	switch {
	case err == nil:
		return ""
	case os.IsNotExist(err):
		return "missing"
	case errors.Is(err, syscall.EIO):
		return "unreadable (I/O error)"
	default:
		return "unreadable"
	}
}
