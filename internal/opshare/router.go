package opshare

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/store"
)

const (
	// flushEvery is how often the counts go to disk. A crash loses at most
	// this much accounting, never a share: the pool's own dashboard is the
	// authority on what was credited.
	flushEvery = time.Minute
	// decisionTTL is how long the rules' answer for a device is reused.
	decisionTTL = 30 * time.Second
	// tripAfter is how many operator-side rejections in a row, without an
	// accepted one in between, make the pool's share switch itself off: shares
	// the pool does not credit to the operator are shares the customer lost.
	tripAfter = 5
	// tripCooldown is how long a pool stays off after that.
	tripCooldown = 30 * time.Minute
)

// Router implements Splitter: it applies the schedule, keeps the accounts and
// guards the customer against a pool that does not honour the operator's
// worker.
type Router struct {
	cfg      func() Config
	rules    Rules
	consents Consents
	dir      string
	now      func() time.Time
	// OnTrip is called when a pool's share switches itself off. It must not
	// block. May be nil.
	OnTrip func(poolID, reason string)

	// flushMu serializes writers: two flushes would each read a day's file,
	// add their counts and write it back, and the later write would drop the
	// earlier one.
	flushMu sync.Mutex

	mu     sync.Mutex
	deltas map[string]*row // "day|device" -> counts not written yet
	cache  map[string]cached
	pools  map[string]*poolState
}

type cached struct {
	d     Decision
	until time.Time
}

type poolState struct {
	consecutive int
	trippedAt   time.Time
	until       time.Time
	reason      string
}

// row is the persisted account of one device and day.
type row struct {
	CustomerID string `json:"customer_id"`
	DeviceID   string `json:"device_id"`
	RuleID     string `json:"rule_id,omitempty"`

	OperatorAccepted   uint64  `json:"operator_accepted,omitempty"`
	OperatorRejected   uint64  `json:"operator_rejected,omitempty"`
	OperatorDifficulty float64 `json:"operator_difficulty,omitempty"`
	CustomerAccepted   uint64  `json:"customer_accepted,omitempty"`
	CustomerRejected   uint64  `json:"customer_rejected,omitempty"`
	CustomerDifficulty float64 `json:"customer_difficulty,omitempty"`

	// Windows maps a window's start (Unix seconds) to its length in seconds.
	// It is a set: writing the same window twice keeps one.
	Windows map[string]int64 `json:"windows,omitempty"`
}

type dayFile struct {
	Rows map[string]*row `json:"rows"`
}

func normDay(f *dayFile) {
	if f.Rows == nil {
		f.Rows = map[string]*row{}
	}
}

func (r *row) add(o *row) {
	if o.RuleID != "" {
		r.RuleID = o.RuleID
	}
	r.OperatorAccepted += o.OperatorAccepted
	r.OperatorRejected += o.OperatorRejected
	r.OperatorDifficulty += o.OperatorDifficulty
	r.CustomerAccepted += o.CustomerAccepted
	r.CustomerRejected += o.CustomerRejected
	r.CustomerDifficulty += o.CustomerDifficulty
	for k, v := range o.Windows {
		if r.Windows == nil {
			r.Windows = map[string]int64{}
		}
		r.Windows[k] = v
	}
}

// NewRouter opens the accounts under dir. rules and consents are required;
// cfg is read on every call.
func NewRouter(dir string, cfg func() Config, rules Rules, consents Consents) *Router {
	return &Router{
		cfg: cfg, rules: rules, consents: consents, dir: dir, now: time.Now,
		deltas: map[string]*row{}, cache: map[string]cached{}, pools: map[string]*poolState{},
	}
}

// Active reports whether the proxy-wide switch is on.
func (r *Router) Active() bool { return r.cfg().Enabled }

func (r *Router) decision(deviceID, customerID string, now time.Time) Decision {
	r.mu.Lock()
	c, ok := r.cache[deviceID]
	r.mu.Unlock()
	if ok && now.Before(c.until) {
		return c.d
	}
	d := r.rules.Share(Subject{CustomerID: customerID, DeviceID: deviceID}, now)
	if m := r.cfg().max(); d.Fraction > m {
		d.Fraction = m
	}
	if d.Fraction < 0 {
		d.Fraction = 0
	}
	r.mu.Lock()
	r.cache[deviceID] = cached{d: d, until: now.Add(decisionTTL)}
	r.mu.Unlock()
	return d
}

func (r *Router) tripped(poolID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pools[poolID]
	return p != nil && now.Before(p.until)
}

// Eligible implements Splitter.
func (r *Router) Eligible(poolID, deviceID, customerID string) bool {
	if !r.cfg().Enabled {
		return false
	}
	now := r.now()
	if r.tripped(poolID, now) {
		return false
	}
	if customerID == "" || !r.consents.Consented(customerID) {
		return false
	}
	return r.decision(deviceID, customerID, now).Fraction > 0
}

// Route implements Splitter.
func (r *Router) Route(poolID, deviceID, customerID string, now time.Time) Route {
	if !r.Eligible(poolID, deviceID, customerID) {
		return Route{}
	}
	d := r.decision(deviceID, customerID, now)
	w := WindowAt(deviceID, d.Fraction, r.cfg().cycle(), now)
	if !w.In {
		return Route{}
	}
	return Route{Operator: true, Window: w.Start}
}

// Note implements Splitter.
func (r *Router) Note(o Outcome) {
	if o.DeviceID == "" {
		return
	}
	at := o.At
	if at.IsZero() {
		at = r.now()
	}
	at = at.UTC()
	key := at.Format("2006-01-02") + "|" + o.DeviceID

	r.mu.Lock()
	defer r.mu.Unlock()
	rw := r.deltas[key]
	if rw == nil {
		rw = &row{CustomerID: o.CustomerID, DeviceID: o.DeviceID}
		r.deltas[key] = rw
	}
	if c, ok := r.cache[o.DeviceID]; ok {
		rw.RuleID = c.d.RuleID
	}
	if o.Operator {
		if o.Accepted {
			rw.OperatorAccepted++
			rw.OperatorDifficulty += o.Difficulty
		} else {
			rw.OperatorRejected++
		}
		if rw.Windows == nil {
			rw.Windows = map[string]int64{}
		}
		// The length of a window is cycle × fraction; it is recomputed rather
		// than carried in the outcome so that the accounting and the schedule
		// cannot disagree.
		if c, ok := r.cache[o.DeviceID]; ok {
			cycle := r.cfg().cycle()
			rw.Windows[strconv.FormatInt(o.Window, 10)] = int64(time.Duration(c.d.Fraction*float64(cycle)).Round(time.Second) / time.Second)
		}
		r.breakerLocked(o, at)
		return
	}
	if o.Accepted {
		rw.CustomerAccepted++
		rw.CustomerDifficulty += o.Difficulty
	} else {
		rw.CustomerRejected++
	}
}

// breakerLocked counts operator-side rejections. A pool that keeps rejecting
// the operator's shares, without crediting one, is not honouring the second
// worker: the share is switched off on it so that the customer stops losing
// shares.
func (r *Router) breakerLocked(o Outcome, at time.Time) {
	p := r.pools[o.PoolID]
	if p == nil {
		p = &poolState{}
		r.pools[o.PoolID] = p
	}
	if o.Accepted {
		p.consecutive = 0
		return
	}
	p.consecutive++
	if p.consecutive < tripAfter {
		return
	}
	reason := o.Reason
	if reason == "" {
		reason = "shares of the operator's worker are rejected"
	}
	r.tripLocked(o.PoolID, p, at, fmt.Sprintf("%d shares in a row rejected: %s", p.consecutive, reason))
}

func (r *Router) tripLocked(poolID string, p *poolState, at time.Time, reason string) {
	p.consecutive = 0
	p.trippedAt, p.until, p.reason = at, at.Add(tripCooldown), reason
	slog.Warn("operator share switched off on a pool", "pool", poolID, "reason", reason, "for", tripCooldown.String())
	if r.OnTrip != nil {
		go r.OnTrip(poolID, reason)
	}
}

// OperatorRefused implements Splitter.
func (r *Router) OperatorRefused(poolID, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.pools[poolID]
	if p == nil {
		p = &poolState{}
		r.pools[poolID] = p
	}
	if r.now().Before(p.until) {
		return // already off; do not extend the cooldown by every new session
	}
	r.tripLocked(poolID, p, r.now(), "the pool did not accept the operator's worker: "+reason)
}

// PoolStatus is the state of the share on one pool.
type PoolStatus struct {
	PoolID string    `json:"pool_id"`
	Off    bool      `json:"off"`
	Since  time.Time `json:"since,omitempty"`
	Until  time.Time `json:"until,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// Pools lists the pools on which the share switched itself off.
func (r *Router) Pools() []PoolStatus {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []PoolStatus
	for id, p := range r.pools {
		if now.Before(p.until) {
			out = append(out, PoolStatus{PoolID: id, Off: true, Since: p.trippedAt, Until: p.until, Reason: p.reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PoolID < out[j].PoolID })
	return out
}

// ---- persistence ----

func (r *Router) dayPath(day string) string { return filepath.Join(r.dir, day+".json") }

// Flush writes the counts not yet written. A failed day is kept for the next
// try, so nothing is lost and nothing is written twice.
func (r *Router) Flush() error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.mu.Lock()
	take := r.deltas
	r.deltas = map[string]*row{}
	r.mu.Unlock()
	if len(take) == 0 {
		return nil
	}
	byDay := map[string]map[string]*row{}
	for k, v := range take {
		day, dev := k[:10], k[11:]
		if byDay[day] == nil {
			byDay[day] = map[string]*row{}
		}
		byDay[day][dev] = v
	}
	var firstErr error
	for day, rows := range byDay {
		err := r.writeDay(day, rows)
		if err == nil {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
		r.mu.Lock()
		for dev, v := range rows {
			k := day + "|" + dev
			if cur := r.deltas[k]; cur != nil {
				cur.add(v)
			} else {
				r.deltas[k] = v
			}
		}
		r.mu.Unlock()
	}
	return firstErr
}

func (r *Router) writeDay(day string, rows map[string]*row) error {
	doc, err := store.OpenDoc[dayFile](r.dayPath(day), normDay)
	if err != nil {
		return err
	}
	return doc.Mutate(func(f *dayFile) error {
		for dev, v := range rows {
			cur := f.Rows[dev]
			if cur == nil {
				cur = &row{CustomerID: v.CustomerID, DeviceID: v.DeviceID}
				f.Rows[dev] = cur
			}
			cur.add(v)
		}
		return nil
	}, nil)
}

// Run flushes every minute and once more when ctx ends.
func (r *Router) Run(ctx context.Context) {
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := r.Flush(); err != nil {
				slog.Warn("operator share accounts not written", "err", err)
			}
			return
		case <-t.C:
			if err := r.Flush(); err != nil {
				slog.Warn("operator share accounts not written", "err", err)
			}
		}
	}
}

// Usage implements UsageReader.
func (r *Router) Usage(from, to time.Time, customerID string) []DayUsage {
	from, to = from.UTC(), to.UTC()
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	if end.Before(start) {
		return nil
	}
	// A year is far more than a report needs and keeps a typo from reading
	// decades of files.
	if end.Sub(start) > 400*24*time.Hour {
		start = end.Add(-400 * 24 * time.Hour)
	}
	var out []DayUsage
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		merged := map[string]*row{}
		if doc, err := store.OpenDoc[dayFile](r.dayPath(day), normDay); err == nil {
			doc.Read(func(f *dayFile) {
				for dev, v := range f.Rows {
					c := &row{CustomerID: v.CustomerID, DeviceID: v.DeviceID}
					c.add(v)
					merged[dev] = c
				}
			})
		}
		r.mu.Lock()
		for k, v := range r.deltas {
			if k[:10] != day {
				continue
			}
			dev := k[11:]
			c := merged[dev]
			if c == nil {
				c = &row{CustomerID: v.CustomerID, DeviceID: v.DeviceID}
				merged[dev] = c
			}
			c.add(v)
		}
		r.mu.Unlock()
		for _, rw := range merged {
			if customerID != "" && rw.CustomerID != customerID {
				continue
			}
			u := DayUsage{
				Day: day, CustomerID: rw.CustomerID, DeviceID: rw.DeviceID, RuleID: rw.RuleID,
				OperatorAccepted: rw.OperatorAccepted, OperatorRejected: rw.OperatorRejected, OperatorDifficulty: rw.OperatorDifficulty,
				CustomerAccepted: rw.CustomerAccepted, CustomerRejected: rw.CustomerRejected, CustomerDifficulty: rw.CustomerDifficulty,
				Windows: len(rw.Windows),
			}
			for _, secs := range rw.Windows {
				u.AllocatedSeconds += secs
			}
			out = append(out, u)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].DeviceID < out[j].DeviceID
	})
	return out
}
