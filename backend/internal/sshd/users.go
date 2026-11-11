package sshd

// The accounts that can log in over SSH, and the keys each lets in. Nightjar runs as root, and each
// account's ~/.ssh is that user's to change, so a key is written as root without trusting anything
// there: no following a symbolic link the user put in place of ~/.ssh or authorized_keys (which
// could point root's write at /etc/shadow), no writing through another name of a file, and the new
// file put in place whole by a rename, then given to the user.

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// User is an account that can log in over SSH, and its authorized keys.
type User struct {
	Name         string   `json:"name"`
	Keys         int      `json:"keys"`
	Fingerprints []string `json:"fingerprints"`
}

// account is a line of the passwd file.
type account struct {
	name     string
	uid, gid int
	home     string
	shell    string
}

// noLogin are shells that turn a login away.
var noLogin = map[string]bool{"nologin": true, "false": true, "sync": true, "halt": true, "shutdown": true}

// accounts are the accounts that can log in over SSH: root, and people's accounts (uid 1000 and up)
// with a login shell and a home folder that is there. System accounts are left out: a key added to
// one would be a way in nobody expects.
func (m *Manager) accounts() []account {
	b, err := os.ReadFile(m.passwd())
	if err != nil {
		return nil
	}
	var root []account
	var people []account
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) != 7 || strings.HasPrefix(f[0], "#") || f[0] == "" {
			continue
		}
		uid, err1 := strconv.Atoi(f[2])
		gid, err2 := strconv.Atoi(f[3])
		if err1 != nil || err2 != nil {
			continue
		}
		a := account{name: f[0], uid: uid, gid: gid, home: f[5], shell: f[6]}
		if !filepath.IsAbs(a.home) {
			continue
		}
		if st, err := os.Stat(a.home); err != nil || !st.IsDir() {
			continue
		}
		switch {
		case uid == 0 && a.name == "root":
			root = append(root, a)
		case uid >= 1000 && uid != 65534 && a.shell != "" && !noLogin[filepath.Base(a.shell)]:
			people = append(people, a)
		}
	}
	return append(root, people...)
}

func (m *Manager) account(name string) (account, error) {
	for _, a := range m.accounts() {
		if a.name == name {
			return a, nil
		}
	}
	return account{}, fmt.Errorf("there is no account %q here that can log in over SSH (root, or a person's account with a login shell and a home folder)", clip(name, 64))
}

// users are the accounts that can log in, with the keys each has.
func (m *Manager) users() []User {
	users := []User{}
	for _, a := range m.accounts() {
		u := User{Name: a.name, Fingerprints: []string{}}
		for _, k := range readKeys(a.home) {
			u.Fingerprints = append(u.Fingerprints, k.Fingerprint())
		}
		u.Keys = len(u.Fingerprints)
		users = append(users, u)
	}
	return users
}

// readKeys are the keys in a home folder's .ssh/authorized_keys, read without leaving the folder.
func readKeys(home string) []Key {
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil
	}
	defer root.Close()
	f, err := root.Open(".ssh/authorized_keys")
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxKeysFile))
	return keysIn(b)
}

// maxKeysFile is the most of an authorized_keys file read.
const maxKeysFile = 1 << 20

// errSymlink is ~/.ssh or authorized_keys being a symbolic link.
func errSymlink(user, name string) error {
	return fmt.Errorf("%s's %s is a symbolic link; Nightjar won't write through one, as root, into a file the account chose. Replace it with the real file and try again", user, name)
}

// writeKey adds a key to an account's authorized_keys, unless the same key is there already.
func writeKey(a account, k Key) error {
	root, err := os.OpenRoot(a.home)
	if err != nil {
		return fmt.Errorf("could not open %s's home folder: %w", a.name, err)
	}
	defer root.Close()

	// ~/.ssh: made if missing, the account's own and closed to others, as sshd wants it.
	made := false
	st, err := root.Lstat(".ssh")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := root.Mkdir(".ssh", 0o700); err != nil {
			return fmt.Errorf("could not make %s's .ssh folder: %w", a.name, err)
		}
		made = true
		if st, err = root.Lstat(".ssh"); err != nil {
			return err
		}
	case err != nil:
		return err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return errSymlink(a.name, "~/.ssh")
	}
	if !st.IsDir() {
		return fmt.Errorf("%s's ~/.ssh is not a folder", a.name)
	}
	dir, err := root.OpenRoot(".ssh")
	if err != nil {
		return err
	}
	defer dir.Close()
	// The folder opened must be the one just looked at, not something swapped in since.
	d, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	if dst, err := d.Stat(); err != nil || !os.SameFile(st, dst) {
		return fmt.Errorf("%s's ~/.ssh changed while it was being written; try again", a.name)
	}
	if made {
		if err := d.Chown(a.uid, a.gid); err != nil {
			return fmt.Errorf("could not give %s its .ssh folder: %w", a.name, err)
		}
		if err := d.Chmod(0o700); err != nil {
			return err
		}
	}

	// The keys there now, kept as they are.
	var old []byte
	if st, err := dir.Lstat("authorized_keys"); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 {
			return errSymlink(a.name, "~/.ssh/authorized_keys")
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("%s's ~/.ssh/authorized_keys is not a plain file", a.name)
		}
		// A file with other names, or someone else's, may be another file entirely (a hard link to
		// /etc/shadow): copying it into the account's new file would hand its contents over.
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && (sys.Nlink > 1 || (int(sys.Uid) != a.uid && sys.Uid != 0)) {
			return fmt.Errorf("%s's ~/.ssh/authorized_keys has other names or belongs to another account; Nightjar won't copy it. Fix it by hand and try again", a.name)
		}
		f, err := dir.Open("authorized_keys")
		if err != nil {
			return err
		}
		fst, err := f.Stat()
		if err == nil && !os.SameFile(st, fst) {
			err = fmt.Errorf("%s's authorized_keys changed while it was being read; try again", a.name)
		}
		if err == nil {
			old, err = io.ReadAll(io.LimitReader(f, maxKeysFile+1))
		}
		f.Close()
		if err != nil {
			return err
		}
		if len(old) > maxKeysFile {
			return fmt.Errorf("%s's authorized_keys is over a megabyte; Nightjar won't rewrite it", a.name)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, have := range keysIn(old) {
		if bytes.Equal(have.Blob, k.Blob) {
			return nil
		}
	}

	// The new file, whole, then renamed over the old: sshd never reads half of it.
	next := old
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	next = append(next, k.Line()+"\n"...)
	var rnd [6]byte
	rand.Read(rnd[:])
	tmp := ".authorized_keys.nightjar-" + hex.EncodeToString(rnd[:])
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("could not write %s's authorized_keys: %w", a.name, err)
	}
	_, err = f.Write(next)
	if err == nil {
		err = f.Chown(a.uid, a.gid)
	}
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = dir.Rename(tmp, "authorized_keys")
	}
	if err != nil {
		dir.Remove(tmp)
		return fmt.Errorf("could not write %s's authorized_keys: %w", a.name, err)
	}
	return nil
}
