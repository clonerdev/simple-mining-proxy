package audit

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) (*Log, *time.Time) {
	t.Helper()
	l, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	return l, &now
}

func TestEntriesAreKeptNewestFirst(t *testing.T) {
	l, now := open(t)
	for i := 0; i < 5; i++ {
		*now = now.Add(time.Minute)
		l.Add(Entry{Actor: Actor{Type: ActorUser, Name: "ali"}, Action: "device.approve", Target: Target{Type: "device", ID: fmt.Sprint("d", i)}})
	}
	got, err := l.Query(Query{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Target.ID != "d4" || got[2].Target.ID != "d2" {
		t.Fatalf("entries = %+v", got)
	}
	if got[0].Result != OK || got[0].ID == "" || got[0].ID == got[1].ID {
		t.Fatalf("id/result not filled: %+v", got[:2])
	}
}

func TestSecretsNeverReachTheLog(t *testing.T) {
	l, _ := open(t)
	l.Add(Entry{Actor: Actor{Type: ActorEnvAdmin, Name: "admin"}, Action: "user.create", Details: map[string]any{
		"username":     "sara",
		"password":     "hunter2hunter2",
		"api_token":    "abc",
		"access_key":   "k-123",
		"nested":       map[string]any{"Authorization": "Bearer xyz", "ok": "fine"},
		"long":         strings.Repeat("я", 1000),
		"passwordHash": "pbkdf2$...",
	}})
	got, _ := l.Query(Query{})
	if len(got) != 1 {
		t.Fatalf("entries = %d", len(got))
	}
	d := got[0].Details
	for _, k := range []string{"password", "api_token", "access_key", "passwordHash"} {
		if d[k] != "***" {
			t.Errorf("%s = %v, want ***", k, d[k])
		}
	}
	if n := d["nested"].(map[string]any); n["Authorization"] != "***" || n["ok"] != "fine" {
		t.Errorf("nested = %v", n)
	}
	if s := d["long"].(string); len([]rune(s)) != maxString+1 {
		t.Errorf("long text has %d runes", len([]rune(s)))
	}
	if d["username"] != "sara" {
		t.Errorf("username = %v", d["username"])
	}
}

func TestQueryFilters(t *testing.T) {
	l, now := open(t)
	add := func(actor, action, targetType, targetID, result string) {
		*now = now.Add(time.Second)
		l.Add(Entry{Actor: Actor{Type: ActorUser, Name: actor}, Action: action, Target: Target{Type: targetType, ID: targetID}, Result: result})
	}
	add("ali", "device.approve", "device", "acme.s21-1", OK)
	add("ali", "device.block", "device", "acme.s21-2", OK)
	add("sara", "device.block", "device", "beta.x1", Denied)
	add("sara", "user.create", "user", "bob", OK)

	n := func(q Query) int { got, _ := l.Query(q); return len(got) }
	for name, tc := range map[string]struct {
		q    Query
		want int
	}{
		"all":                {Query{}, 4},
		"actor":              {Query{Actor: "SARA"}, 2},
		"action prefix":      {Query{Action: "device."}, 3},
		"exact action":       {Query{Action: "device.block"}, 2},
		"target type":        {Query{Target: "user"}, 1},
		"target type and id": {Query{Target: "device:beta.x1"}, 1},
		"result":             {Query{Result: Denied}, 1},
		"text":               {Query{Text: "ACME"}, 2},
	} {
		if got := n(tc.q); got != tc.want {
			t.Errorf("%s: %d entries, want %d", name, got, tc.want)
		}
	}
}

func TestPagingWithBefore(t *testing.T) {
	l, now := open(t)
	for i := 0; i < 10; i++ {
		*now = now.Add(time.Second)
		l.Add(Entry{Actor: System, Action: "tick", Target: Target{ID: fmt.Sprint(i)}})
	}
	first, _ := l.Query(Query{Limit: 4})
	second, _ := l.Query(Query{Limit: 4, Before: first[len(first)-1].Time})
	if first[0].Target.ID != "9" || second[0].Target.ID != "5" || second[3].Target.ID != "2" {
		t.Fatalf("pages: %v then %v", ids(first), ids(second))
	}
}

func ids(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Target.ID)
	}
	return out
}

func TestOldEntriesStayOutOfTheDefaultRange(t *testing.T) {
	l, now := open(t)
	*now = now.AddDate(0, -3, 0)
	l.Add(Entry{Actor: System, Action: "old"})
	*now = now.AddDate(0, 3, 0)
	l.Add(Entry{Actor: System, Action: "new"})
	if got, _ := l.Query(Query{}); len(got) != 1 || got[0].Action != "new" {
		t.Fatalf("default range = %+v", got)
	}
	if got, _ := l.Query(Query{From: now.AddDate(-1, 0, 0)}); len(got) != 2 {
		t.Fatalf("wide range = %d entries", len(got))
	}
}

func TestNilLogIsHarmless(t *testing.T) {
	var l *Log
	l.Add(Entry{Action: "x"})
	l.OnAdd(func(Entry) {})
	l.Prune(time.Hour)
	if got, err := l.Query(Query{}); got != nil || err != nil {
		t.Fatalf("%v %v", got, err)
	}
}

func TestHookSeesTheStoredEntry(t *testing.T) {
	l, _ := open(t)
	var seen Entry
	l.OnAdd(func(e Entry) { seen = e })
	l.Add(Entry{Actor: System, Action: "x", Details: map[string]any{"token": "t"}})
	if seen.Action != "x" || seen.Details["token"] != "***" {
		t.Fatalf("hook entry = %+v", seen)
	}
}
