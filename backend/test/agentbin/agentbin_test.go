package agentbin_test

import (
	"bytes"
	"io"
	"testing"

	"nightjar/internal/agentbin"
)

// The builds are embedded by `make agent`; a plain `go test` may have none, and must say so.
func TestEmbeddedAgentsUnpackToLinuxBinaries(t *testing.T) {
	if _, _, err := agentbin.Open("x86"); err != agentbin.ErrNotBuilt {
		t.Fatalf("an unknown architecture: %v", err)
	}
	builds := agentbin.Builds()
	if len(builds) == 0 {
		if _, _, err := agentbin.Open("amd64"); err != agentbin.ErrNotBuilt {
			t.Fatalf("without builds, opening one says how to make them: %v", err)
		}
		t.Skip("no agent builds embedded; `make agent` adds them")
	}
	for _, b := range builds {
		rc, got, err := agentbin.Open(b.Arch)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || int64(len(raw)) != b.Size || got != b || b.File != "nightjar-agent-linux-"+b.Arch {
			t.Fatalf("%s: %d bytes, listed %+v: %v", b.Arch, len(raw), b, err)
		}
		if !bytes.HasPrefix(raw, []byte("\x7fELF")) {
			t.Fatalf("%s is not a Linux binary", b.Arch)
		}
	}
}
