package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type doc struct {
	Names map[string]int `json:"names"`
	List  []string       `json:"list"`
}

func normDoc(d *doc) {
	if d.Names == nil {
		d.Names = map[string]int{}
	}
}

func TestDocPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "doc.json")
	d, err := OpenDoc(path, normDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Mutate(func(v *doc) error { v.Names["a"] = 1; v.List = append(v.List, "x"); return nil }, nil); err != nil {
		t.Fatal(err)
	}
	again, err := OpenDoc(path, normDoc)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Clone()
	if got.Names["a"] != 1 || len(got.List) != 1 {
		t.Fatalf("reloaded document = %+v", got)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, %v; want 0600", st.Mode().Perm(), err)
	}
}

func TestDocFailedMutationChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	d, _ := OpenDoc(path, normDoc)
	_ = d.Mutate(func(v *doc) error { v.Names["a"] = 1; return nil }, nil)
	boom := errors.New("boom")
	if err := d.Mutate(func(v *doc) error { v.Names["a"] = 2; return boom }, nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if d.Clone().Names["a"] != 1 {
		t.Fatal("a failed mutation changed the document")
	}
	// A write failure (the directory is gone) must not change it either.
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := d.Mutate(func(v *doc) error { v.Names["a"] = 3; return nil }, nil); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
	if d.Clone().Names["a"] != 1 {
		t.Fatal("a failed write changed the document")
	}
}

func TestDocRefusesACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDoc(path, normDoc); err == nil || !strings.Contains(err.Error(), "never reset") {
		t.Fatalf("err = %v", err)
	}
}

func TestDocCommitSeesTheNewDocument(t *testing.T) {
	d, _ := OpenDoc(filepath.Join(t.TempDir(), "doc.json"), normDoc)
	var seen int
	_ = d.Mutate(func(v *doc) error { v.Names["a"] = 7; return nil }, func(v *doc) { seen = v.Names["a"] })
	if seen != 7 {
		t.Fatalf("commit saw %d", seen)
	}
}

type rec struct {
	T time.Time `json:"t"`
	N int       `json:"n"`
}

func collect(t *testing.T, l *Lines, from, to time.Time) []int {
	t.Helper()
	var out []int
	err := l.Read(from, to, func(line []byte) bool {
		var r rec
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("bad line %q: %v", line, err)
		}
		out = append(out, r.N)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLinesSplitByPeriodAndReadInOrder(t *testing.T) {
	l, err := NewLines(t.TempDir(), "log", Monthly, true)
	if err != nil {
		t.Fatal(err)
	}
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 12, 0, 0, 0, time.UTC) }
	for i, at := range []time.Time{day(1, 5), day(1, 20), day(2, 3), day(3, 9)} {
		if err := l.Append(at, rec{T: at, N: i}); err != nil {
			t.Fatal(err)
		}
	}
	if got := collect(t, l, time.Time{}, time.Time{}); len(got) != 4 || got[0] != 0 || got[3] != 3 {
		t.Fatalf("all = %v", got)
	}
	// February only: whole files are selected, the caller filters inside them.
	if got := collect(t, l, day(2, 1), day(2, 28)); len(got) != 1 || got[0] != 2 {
		t.Fatalf("february = %v", got)
	}
	n, err := l.Prune(day(3, 5))
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v; want the January and February files", n, err)
	}
	if got := collect(t, l, time.Time{}, time.Time{}); len(got) != 1 || got[0] != 3 {
		t.Fatalf("after prune = %v", got)
	}
}

func TestLinesSkipATornLineAndRecover(t *testing.T) {
	dir := t.TempDir()
	l, _ := NewLines(dir, "log", Daily, false)
	at := time.Date(2026, 5, 1, 1, 0, 0, 0, time.UTC)
	_ = l.Append(at, rec{T: at, N: 1})
	_ = l.Close()
	// Simulate a crash in the middle of a write.
	f, err := os.OpenFile(filepath.Join(dir, "log-2026-05-01.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"t":"2026-05-01T01:00:00Z","n":`)
	f.Close()

	l2, _ := NewLines(dir, "log", Daily, false)
	if err := l2.Append(at, rec{T: at, N: 2}); err != nil {
		t.Fatal(err)
	}
	if got := collect(t, l2, time.Time{}, time.Time{}); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("lines = %v; the torn line must be skipped and the next record kept", got)
	}
}

func TestLinesStopWhenTheCallbackSaysSo(t *testing.T) {
	l, _ := NewLines(t.TempDir(), "log", Daily, false)
	at := time.Date(2026, 5, 1, 1, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		_ = l.Append(at, rec{T: at, N: i})
	}
	n := 0
	_ = l.Read(time.Time{}, time.Time{}, func([]byte) bool { n++; return n < 3 })
	if n != 3 {
		t.Fatalf("callback ran %d times", n)
	}
}
