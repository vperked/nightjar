// Package record holds the few storage types that code outside the main monitor shares. It imports
// nothing heavy: the agent keeps its update targets in a file of its own, and must not carry the
// SQLite driver just to name these.
package record

import "errors"

// ErrUnavailable is returned by writes when the database could not be opened.
var ErrUnavailable = errors.New("database unavailable")

// UpdateTarget is one stored update target: its definition and its last known state (both JSON).
type UpdateTarget struct {
	ID          string
	Spec, State []byte
}
