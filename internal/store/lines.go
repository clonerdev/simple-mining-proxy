package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Granularity of the files of a Lines log.
const (
	Monthly = "2006-01"
	Daily   = "2006-01-02"
)

// maxLine bounds one line when reading; longer ones are skipped.
const maxLine = 1 << 20

// Lines is an append-only JSON-lines log: one file per month or day (UTC),
// one record per line. A crash can leave one unreadable line at the end of a
// file; readers skip it and the next append starts on a fresh line.
type Lines struct {
	dir, prefix, layout string
	sync                bool

	mu   sync.Mutex
	f    *os.File
	name string
}

// NewLines keeps the log in dir as <prefix>-<period>.jsonl. With sync every
// append is flushed to disk before it returns: for records that must survive
// a power loss, such as the ledger and the audit log.
func NewLines(dir, prefix, layout string, sync bool) (*Lines, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	return &Lines{dir: dir, prefix: prefix, layout: layout, sync: sync}, nil
}

func (l *Lines) fileFor(t time.Time) string {
	return filepath.Join(l.dir, l.prefix+"-"+t.UTC().Format(l.layout)+".jsonl")
}

// Append writes v as one line into the file of the period of t.
func (l *Lines) Append(t time.Time, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	name := l.fileFor(t)
	if l.f == nil || l.name != name {
		if l.f != nil {
			l.f.Close()
			l.f = nil
		}
		f, err := os.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		// A file that does not end with a newline holds a torn last line.
		if st, err := f.Stat(); err == nil && st.Size() > 0 {
			if last, err := readLastByte(name); err == nil && last != '\n' {
				b = append([]byte{'\n'}, b...)
			}
		}
		l.f, l.name = f, name
	}
	if _, err := l.f.Write(b); err != nil {
		l.f.Close()
		l.f = nil
		return err
	}
	if l.sync {
		return l.f.Sync()
	}
	return nil
}

func readLastByte(name string) (byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return 0, errors.New("empty")
	}
	var one [1]byte
	if _, err := f.ReadAt(one[:], st.Size()-1); err != nil {
		return 0, err
	}
	return one[0], nil
}

// Close closes the open file. Appending again reopens it.
func (l *Lines) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

type periodFile struct {
	name  string
	start time.Time
}

func (l *Lines) periods() ([]periodFile, error) {
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []periodFile
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasPrefix(n, l.prefix+"-") || !strings.HasSuffix(n, ".jsonl") {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(n, l.prefix+"-"), ".jsonl")
		start, err := time.Parse(l.layout, stamp)
		if err != nil {
			continue
		}
		out = append(out, periodFile{filepath.Join(l.dir, n), start.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, nil
}

func (l *Lines) periodEnd(start time.Time) time.Time {
	if l.layout == Monthly {
		return start.AddDate(0, 1, 0)
	}
	return start.AddDate(0, 0, 1)
}

// Read calls fn for every line of the files that overlap [from, to], oldest
// first, until fn returns false. The caller filters by the time inside the
// records: a file covers a whole period. Unreadable lines are skipped.
func (l *Lines) Read(from, to time.Time, fn func(line []byte) bool) error {
	files, err := l.periods()
	if err != nil {
		return err
	}
	for _, p := range files {
		if !to.IsZero() && !p.start.Before(to) {
			break
		}
		if !from.IsZero() && !l.periodEnd(p.start).After(from) {
			continue
		}
		cont, err := readFile(p.name, fn)
		if err != nil {
			return err
		}
		if !cont {
			return nil
		}
	}
	return nil
}

func readFile(name string, fn func([]byte) bool) (bool, error) {
	f, err := os.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && len(line) <= maxLine {
			if t := bytes.TrimSpace(line); len(t) > 0 && json.Valid(t) {
				if !fn(t) {
					return false, nil
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// Prune deletes whole files that end before cutoff and returns how many.
func (l *Lines) Prune(cutoff time.Time) (int, error) {
	files, err := l.periods()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, p := range files {
		if !l.periodEnd(p.start).After(cutoff) {
			l.mu.Lock()
			if l.name == p.name && l.f != nil {
				l.f.Close()
				l.f = nil
			}
			l.mu.Unlock()
			if err := os.Remove(p.name); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}
