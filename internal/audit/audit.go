// Package audit keeps the log of who did what in the admin panel and in the
// device controls. It is append-only JSON lines, one file per month, flushed
// to disk on every entry, and it never stores a password, token or key.
package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/famfamfam/simple-mining-proxy/internal/store"
)

// Actor types.
const (
	ActorUser      = "user"
	ActorEnvAdmin  = "env_admin" // the administrator from ADMIN_USERNAME / ADMIN_PASSWORD
	ActorAPIToken  = "api_token"
	ActorSystem    = "system"
	ActorTelegram  = "telegram"
	ActorDevice    = "device"
	ActorAnonymous = "anonymous"
)

// Results.
const (
	OK     = "ok"
	Denied = "denied"
	Failed = "failed"
)

type Actor struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	ID   string `json:"id,omitempty"`
	IP   string `json:"ip,omitempty"`
}

// System is the actor of changes the proxy makes by itself, such as an
// expired device being cut off.
var System = Actor{Type: ActorSystem, Name: "system"}

type Target struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type Entry struct {
	ID      string         `json:"id"`
	Time    time.Time      `json:"time"`
	Actor   Actor          `json:"actor"`
	Action  string         `json:"action"`
	Target  Target         `json:"target,omitempty"`
	Result  string         `json:"result"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Log is the audit log. The zero value and a nil *Log drop entries, which
// keeps code paths that run without a data directory (tests) simple.
type Log struct {
	lines *store.Lines
	now   func() time.Time

	mu   sync.Mutex
	seq  uint64
	hook func(Entry)
}

// Open keeps the log in dir.
func Open(dir string) (*Log, error) {
	lines, err := store.NewLines(dir, "audit", store.Monthly, true)
	if err != nil {
		return nil, err
	}
	return &Log{lines: lines, now: time.Now}, nil
}

// OnAdd registers fn, called after every entry that was written.
func (l *Log) OnAdd(fn func(Entry)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.hook = fn
	l.mu.Unlock()
}

var secretKey = regexp.MustCompile(`(?i)pass|token|secret|key|hash|cookie|authorization|credential`)

const (
	maxString = 300
	maxDepth  = 3
)

// Redact returns a copy of details that is safe to store: the values of keys
// that look like secrets are replaced, and long texts are cut.
func Redact(details map[string]any) map[string]any {
	if len(details) == 0 {
		return nil
	}
	return redactMap(details, 0)
}

func redactMap(m map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if secretKey.MatchString(k) {
			out[k] = "***"
			continue
		}
		out[k] = redactValue(v, depth+1)
	}
	return out
}

func redactValue(v any, depth int) any {
	switch x := v.(type) {
	case string:
		return cut(x)
	case map[string]any:
		if depth >= maxDepth {
			return "…"
		}
		return redactMap(x, depth)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = cut(s)
		}
		return out
	case []any:
		if depth >= maxDepth {
			return "…"
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(e, depth+1)
		}
		return out
	}
	return v
}

func cut(s string) string {
	if utf8.RuneCountInString(s) <= maxString {
		return s
	}
	return string([]rune(s)[:maxString]) + "…"
}

// Add writes an entry. The caller sets Actor, Action and, where it has one,
// Target; Add fills the id, the time and the result (OK by default). A write
// failure is logged and never returned: an operation that already happened
// must not appear to have failed because its record could not be written.
func (l *Log) Add(e Entry) {
	if l == nil || l.lines == nil {
		return
	}
	l.mu.Lock()
	l.seq++
	seq := l.seq
	hook := l.hook
	l.mu.Unlock()

	if e.Time.IsZero() {
		e.Time = l.now()
	}
	e.Time = e.Time.UTC()
	e.ID = fmt.Sprintf("%x-%04x", e.Time.UnixNano(), seq&0xffff)
	if e.Result == "" {
		e.Result = OK
	}
	e.Message = cut(e.Message)
	e.Details = Redact(e.Details)
	if err := l.lines.Append(e.Time, e); err != nil {
		slog.Error("cannot write the audit log", "action", e.Action, "err", err)
		return
	}
	if hook != nil {
		hook(e)
	}
}

// Query selects entries. Zero fields match everything.
type Query struct {
	From, To time.Time // To is exclusive; From defaults to 30 days back
	Before   time.Time // paging: only entries older than this
	Actor    string    // actor name, case-insensitive
	Action   string    // action or action prefix such as "device."
	Target   string    // target type, or "type:id"
	Result   string
	Text     string // substring of the action, actor, target or message
	Limit    int    // at most this many, newest first; 200 by default, 1000 at most
}

// MaxLimit is the most entries one query returns.
const MaxLimit = 1000

func (q Query) match(e *Entry) bool {
	if !q.From.IsZero() && e.Time.Before(q.From) {
		return false
	}
	if !q.To.IsZero() && !e.Time.Before(q.To) {
		return false
	}
	if !q.Before.IsZero() && !e.Time.Before(q.Before) {
		return false
	}
	if q.Actor != "" && !strings.EqualFold(e.Actor.Name, q.Actor) {
		return false
	}
	if q.Action != "" && !strings.HasPrefix(e.Action, q.Action) {
		return false
	}
	if q.Result != "" && e.Result != q.Result {
		return false
	}
	if q.Target != "" {
		typ, id, hasID := strings.Cut(q.Target, ":")
		if e.Target.Type != typ || (hasID && e.Target.ID != id) {
			return false
		}
	}
	if q.Text != "" {
		needle := strings.ToLower(q.Text)
		hay := strings.ToLower(strings.Join([]string{e.Action, e.Actor.Name, e.Target.ID, e.Target.Name, e.Message}, "\n"))
		if !strings.Contains(hay, needle) {
			return false
		}
	}
	return true
}

// Query returns the newest matching entries first.
func (l *Log) Query(q Query) ([]Entry, error) {
	if l == nil || l.lines == nil {
		return nil, nil
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = 200
	case limit > MaxLimit:
		limit = MaxLimit
	}
	if q.From.IsZero() {
		q.From = l.now().AddDate(0, 0, -30)
	}
	to := q.To
	if !q.Before.IsZero() && (to.IsZero() || q.Before.Before(to)) {
		to = q.Before
	}
	// Records are read oldest first; a ring keeps the newest `limit` matches.
	ring := make([]Entry, 0, min(limit, 256))
	start := 0
	err := l.lines.Read(q.From, to, func(line []byte) bool {
		var e Entry
		if json.Unmarshal(line, &e) != nil || !q.match(&e) {
			return true
		}
		if len(ring) < limit {
			ring = append(ring, e)
		} else {
			ring[start] = e
			start = (start + 1) % limit
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ring))
	for i := len(ring) - 1; i >= 0; i-- { // newest first
		out = append(out, ring[(start+i)%len(ring)])
	}
	return out, nil
}

// Prune deletes the files that end before now minus retention.
func (l *Log) Prune(retention time.Duration) {
	if l == nil || l.lines == nil || retention <= 0 {
		return
	}
	if n, err := l.lines.Prune(l.now().Add(-retention)); err != nil {
		slog.Warn("cannot prune the audit log", "err", err)
	} else if n > 0 {
		slog.Info("audit log pruned", "files", n)
	}
}
