// Package selfcheck looks for ways another account could take Nightjar over. It runs as root, so a
// file it runs, serves or reads its settings from, or any folder above one, that someone else owns or
// may change is a way to become root: they swap the file, and Nightjar runs theirs.
package selfcheck

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
)

// Exposed checks each path, and every folder above it up to /, and says in words what another account
// could change. Paths that do not exist are skipped; symlinks are checked where they point as well.
func Exposed(paths []string) []string {
	me := os.Geteuid()
	var out []string
	seen := map[string]bool{}
	check := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		info, err := os.Lstat(p)
		if err != nil {
			return
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return
		}
		if int(st.Uid) != 0 && int(st.Uid) != me {
			out = append(out, fmt.Sprintf("%s belongs to %s", p, owner(st.Uid)))
			return
		}
		// A folder anyone may write to is safe only with the sticky bit (like /tmp), which stops them
		// replacing what they do not own; a file never is. A symlink's own mode means nothing.
		if info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o022 != 0 && !(info.IsDir() && info.Mode()&os.ModeSticky != 0) {
			out = append(out, fmt.Sprintf("%s can be changed by other accounts", p))
		}
	}
	walk := func(p string) {
		for {
			check(p)
			parent := filepath.Dir(p)
			if parent == p {
				return
			}
			p = parent
		}
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(abs); err != nil {
			continue
		}
		walk(abs)
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			walk(real)
		}
	}
	slices.Sort(out)
	return out
}

func owner(uid uint32) string {
	if u, err := user.LookupId(strconv.Itoa(int(uid))); err == nil {
		return "user " + u.Username
	}
	return "user " + strconv.Itoa(int(uid))
}
