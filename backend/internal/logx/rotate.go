package logx

import (
	"fmt"
	"os"
	"sync"
)

// RotatingFile is an io.Writer that rotates at maxBytes, keeping backups as path.1 … path.N.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	f        *os.File // nil after a rotation whose new file could not be opened
	size     int64
}

func OpenRotating(path string, maxBytes int64, backups int) (*RotatingFile, error) {
	r := &RotatingFile{path: path, maxBytes: maxBytes, backups: backups}
	return r, r.open()
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f, r.size = f, 0
	if st != nil {
		r.size = st.Size()
	}
	return nil
}

func (r *RotatingFile) rotate() error {
	r.f.Close()
	r.f = nil
	for i := r.backups - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if r.backups > 0 {
		os.Rename(r.path, r.path+".1")
	} else {
		os.Truncate(r.path, 0)
	}
	return r.open()
}

func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		// The last rotation moved the file away but could not open a new one: only the open is tried
		// again, since rotating once more at every write would push each backup out in turn.
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	// An empty file is not rotated, however big the write: that would only shift the backups along.
	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}
