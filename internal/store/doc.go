// Package store holds the two small building blocks the account, device and
// billing data is kept in. The project has no database and, on purpose, no
// new dependency: a Doc is one JSON document that is replaced atomically, a
// Lines is an append-only JSON-lines log with one file per month or day.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
)

// Doc is a JSON document kept in memory and in a file. Every change is made
// on a copy, validated by the caller, written atomically and only then
// becomes current, exactly like state.json: a failed write changes nothing.
type Doc[T any] struct {
	path      string
	normalize func(*T)

	mu  sync.Mutex
	cur *T
}

// OpenDoc loads the document at path. A missing file is an empty document; a
// file that cannot be parsed is an error, never a silent reset. normalize
// (may be nil) fills defaults, such as nil maps, after loading and before
// every write.
func OpenDoc[T any](path string, normalize func(*T)) (*Doc[T], error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	d := &Doc[T]{path: path, normalize: normalize, cur: new(T)}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	default:
		if err := json.Unmarshal(b, d.cur); err != nil {
			return nil, fmt.Errorf("parse %s: %w (fix or move the file; it is never reset silently)", path, err)
		}
	}
	if normalize != nil {
		normalize(d.cur)
	}
	return d, nil
}

// Read calls fn with the current document under the lock. fn must not keep
// the pointer or change the document.
func (d *Doc[T]) Read(fn func(*T)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d.cur)
}

// Clone returns a deep copy of the current document.
func (d *Doc[T]) Clone() *T {
	d.mu.Lock()
	defer d.mu.Unlock()
	return clone(d.cur)
}

// Mutate applies fn to a copy, writes the copy and makes it current. commit
// (may be nil) runs under the lock after the write, so indexes built from the
// document change in the same order as the file. If fn or the write fails,
// the document is unchanged.
func (d *Doc[T]) Mutate(fn func(*T) error, commit func(*T)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	next := clone(d.cur)
	if err := fn(next); err != nil {
		return err
	}
	if d.normalize != nil {
		d.normalize(next)
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(d.path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("save %s: %w", filepath.Base(d.path), err)
	}
	d.cur = next
	if commit != nil {
		commit(next)
	}
	return nil
}

// Path is the file of the document.
func (d *Doc[T]) Path() string { return d.path }

func clone[T any](v *T) *T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the types stored here are plain data
	}
	c := new(T)
	if err := json.Unmarshal(b, c); err != nil {
		panic(err)
	}
	return c
}
