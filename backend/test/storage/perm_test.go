package storage_test

import (
	"os"
	"path/filepath"
	"testing"

	"nightjar/internal/storage"
)

func TestTheDatabaseIsReadableByItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "n.db")
	os.WriteFile(path, nil, 0o644) // made earlier with a loose umask
	db := storage.Open(path)
	defer db.Close()
	db.SetMeta("k", "v") // so the WAL files exist
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if st, err := os.Stat(p); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", filepath.Base(p), st.Mode().Perm())
		}
	}
}
