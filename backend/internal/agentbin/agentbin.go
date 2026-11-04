// Package agentbin carries the agent's builds inside the monitor, so the dashboard can hand them out
// and they always match its version.
//
// `make agent` writes gzipped builds into dist/ here; a plain `go build` embeds none, and the dashboard
// then says how to build them.
package agentbin

import (
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"slices"
	"sync"
)

//go:embed all:dist
var dist embed.FS

// Arches are the architectures the agent is built for.
var Arches = []string{"amd64", "arm64"}

// Build is one embedded agent.
type Build struct {
	Arch string `json:"arch"`
	File string `json:"file"` // the name to save it as
	Size int64  `json:"size"` // unpacked, in bytes
}

// FileName is what the agent for arch is called.
func FileName(arch string) string { return "nightjar-agent-linux-" + arch }

func path(arch string) string { return "dist/" + FileName(arch) + ".gz" }

// Builds lists the embedded agents.
func Builds() []Build {
	out := []Build{}
	for _, arch := range Arches {
		raw, err := dist.ReadFile(path(arch))
		if err != nil || len(raw) < 4 {
			continue
		}
		// gzip ends with the unpacked size, modulo 4 GiB, which an agent never nears.
		out = append(out, Build{Arch: arch, File: FileName(arch), Size: int64(binary.LittleEndian.Uint32(raw[len(raw)-4:]))})
	}
	return out
}

var ErrNotBuilt = errors.New("this monitor was built without the agent inside; run `make build` (or `make agent`) and restart it")

// Open unpacks the agent for arch as it is read.
func Open(arch string) (io.ReadCloser, Build, error) {
	i := slices.IndexFunc(Builds(), func(b Build) bool { return b.Arch == arch })
	if i < 0 {
		return nil, Build{}, ErrNotBuilt
	}
	f, err := dist.Open(path(arch))
	if err != nil {
		return nil, Build{}, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, Build{}, err
	}
	return readCloser{gz, f}, Builds()[i], nil
}

type readCloser struct {
	*gzip.Reader
	f fs.File
}

func (r readCloser) Close() error {
	r.Reader.Close()
	return r.f.Close()
}

// unpacked caches each build unpacked, with its SHA-256, for sending to agents that update.
var unpacked sync.Map // arch → packed

type packed struct {
	data []byte
	hash string
}

// Load is the agent for arch, unpacked, with its SHA-256 in hex.
func Load(arch string) ([]byte, string, error) {
	if p, ok := unpacked.Load(arch); ok {
		return p.(packed).data, p.(packed).hash, nil
	}
	rc, _, err := Open(arch)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	p := packed{data, hex.EncodeToString(sum[:])}
	unpacked.Store(arch, p)
	return p.data, p.hash, nil
}
