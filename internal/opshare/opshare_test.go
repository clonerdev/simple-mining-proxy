package opshare

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fixedRules struct {
	mu sync.Mutex
	f  float64
	id string
}

func (r *fixedRules) Share(Subject, time.Time) Decision {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Decision{Fraction: r.f, RuleID: r.id}
}

func (r *fixedRules) set(f float64) {
	r.mu.Lock()
	r.f = f
	r.mu.Unlock()
}

type consents struct {
	mu sync.Mutex
	m  map[string]bool
}

func (c *consents) Consented(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[id]
}

func (c *consents) set(id string, v bool) {
	c.mu.Lock()
	c.m[id] = v
	c.mu.Unlock()
}

var base = time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

func TestWindowFractionIsExact(t *testing.T) {
	// One hour a day is 1/24 of the time: 4.1667 %.
	frac := 1.0 / 24
	for _, dev := range []string{"dev-a", "dev-b", "dev-c"} {
		in, total := 0, 0
		// Sampled every quarter second: a window is 37.5 s long, and a coarser
		// grid would measure the grid, not the schedule.
		for s := 0; s < 24*3600*4; s++ {
			w := WindowAt(dev, frac, 15*time.Minute, base.Add(time.Duration(s)*250*time.Millisecond))
			total++
			if w.In {
				in++
			}
		}
		got := float64(in) / float64(total)
		if math.Abs(got-frac) > 0.0002 {
			t.Errorf("%s: window covers %.5f of the day, want %.5f", dev, got, frac)
		}
	}
}

func TestWindowIsDeterministicAndStable(t *testing.T) {
	at := base.Add(7*time.Minute + 13*time.Second)
	a := WindowAt("dev-x", 0.02, 15*time.Minute, at)
	b := WindowAt("dev-x", 0.02, 15*time.Minute, at)
	if a != b {
		t.Fatalf("the same moment gives two answers: %+v %+v", a, b)
	}
	// Every moment of a cycle names the same window.
	first := WindowAt("dev-x", 0.02, 15*time.Minute, base).Start
	for s := 0; s < 900; s += 17 {
		if got := WindowAt("dev-x", 0.02, 15*time.Minute, base.Add(time.Duration(s)*time.Second)).Start; got != first {
			t.Fatalf("window id changes inside one cycle: %d vs %d at +%ds", got, first, s)
		}
	}
	if next := WindowAt("dev-x", 0.02, 15*time.Minute, base.Add(15*time.Minute)).Start; next != first+900 {
		t.Fatalf("next cycle should start 900 s later: %d vs %d", next, first)
	}
}

func TestWindowsSpreadAcrossDevices(t *testing.T) {
	// The devices of a farm must not all be on the operator's worker at once.
	frac := 0.04
	at := func(i int) int {
		n := 0
		for d := 0; d < 200; d++ {
			if WindowAt("device-"+string(rune('A'+d%26))+string(rune('a'+d/26)), frac, 15*time.Minute, base.Add(time.Duration(i)*time.Second)).In {
				n++
			}
		}
		return n
	}
	max := 0
	for i := 0; i < 900; i += 5 {
		if n := at(i); n > max {
			max = n
		}
	}
	if max > 40 { // 20 % of the devices at the busiest moment; 4 % on average
		t.Fatalf("up to %d of 200 devices share at the same time: no spread", max)
	}
}

func TestWindowEdgeCases(t *testing.T) {
	if w := WindowAt("d", 0, 15*time.Minute, base); w.In {
		t.Fatal("zero fraction must never be in")
	}
	if w := WindowAt("d", -1, 15*time.Minute, base); w.In {
		t.Fatal("negative fraction must never be in")
	}
	if w := WindowAt("d", 0.5, 0, base); w.In {
		t.Fatal("no cycle: never in")
	}
	// A full fraction is always in.
	for s := 0; s < 900; s += 31 {
		if !WindowAt("d", 1, 15*time.Minute, base.Add(time.Duration(s)*time.Second)).In {
			t.Fatalf("fraction 1 is out at +%ds", s)
		}
	}
	// Moments before the Unix epoch must not panic or wrap.
	_ = WindowAt("d", 0.1, 15*time.Minute, time.Unix(-12345, 0))
}

func newRouter(t *testing.T, rules Rules, c *consents, enabled bool) (*Router, string) {
	t.Helper()
	dir := t.TempDir()
	r := NewRouter(dir, func() Config { return Config{Enabled: enabled, Cycle: 15 * time.Minute} }, rules, c)
	r.now = func() time.Time { return base.Add(time.Hour) }
	return r, dir
}

// firstInWindow returns a moment at which dev is inside its window.
func firstInWindow(dev string, frac float64) time.Time {
	for s := 0; s < 900; s++ {
		at := base.Add(time.Hour).Add(time.Duration(s) * time.Second)
		if WindowAt(dev, frac, 15*time.Minute, at).In {
			return at
		}
	}
	panic("no window")
}

func TestRouteNeedsEverything(t *testing.T) {
	rules := &fixedRules{f: 0.05, id: "rule-1"}
	c := &consents{m: map[string]bool{}}
	r, _ := newRouter(t, rules, c, true)
	at := firstInWindow("dev1", 0.05)

	if r.Route("p", "dev1", "cust1", at).Operator {
		t.Fatal("routed to the operator without the customer's consent")
	}
	c.set("cust1", true)
	got := r.Route("p", "dev1", "cust1", at)
	if !got.Operator || got.Window == 0 {
		t.Fatalf("with consent and a window the share should go to the operator: %+v", got)
	}
	c.set("cust1", false)
	if r.Route("p", "dev1", "cust1", at).Operator {
		t.Fatal("a withdrawn consent must stop the share at once")
	}
	c.set("cust1", true)
	rules.set(0)
	r.mu.Lock()
	r.cache = map[string]cached{}
	r.mu.Unlock()
	if r.Route("p", "dev1", "cust1", at).Operator {
		t.Fatal("a zero share must never route")
	}
}

func TestSwitchedOffNeverRoutes(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, false)
	if r.Active() || r.Eligible("p", "dev1", "cust1") || r.Route("p", "dev1", "cust1", firstInWindow("dev1", 0.05)).Operator {
		t.Fatal("with the proxy switch off nothing may be routed")
	}
}

func TestFractionIsCapped(t *testing.T) {
	rules := &fixedRules{f: 0.9} // a typo in a rule
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, true)
	d := r.decision("dev1", "cust1", base)
	if d.Fraction > DefaultMaxFraction+1e-9 {
		t.Fatalf("fraction %.2f was not capped to %.2f", d.Fraction, DefaultMaxFraction)
	}
}

func TestNotesAreFlushedOnceAndWindowsCountOnce(t *testing.T) {
	rules := &fixedRules{f: 0.05, id: "rule-1"}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, dir := newRouter(t, rules, c, true)
	at := firstInWindow("dev1", 0.05)
	rt := r.Route("p", "dev1", "cust1", at)
	if !rt.Operator {
		t.Fatal("setup: not in a window")
	}
	for i := 0; i < 3; i++ {
		r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Operator: true, Window: rt.Window, Accepted: true, Difficulty: 100, At: at})
	}
	r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Operator: true, Window: rt.Window, Accepted: false, At: at})
	for i := 0; i < 10; i++ {
		r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Accepted: true, Difficulty: 100, At: at})
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// A second flush with nothing new must not change the totals.
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	// The process restarts in the middle of the same window and sees more
	// shares: the shares add up, the window is still one.
	r2 := NewRouter(dir, func() Config { return Config{Enabled: true, Cycle: 15 * time.Minute} }, rules, c)
	r2.now = r.now
	r2.cache["dev1"] = cached{d: Decision{Fraction: 0.05, RuleID: "rule-1"}, until: at.Add(time.Hour)}
	r2.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Operator: true, Window: rt.Window, Accepted: true, Difficulty: 100, At: at})
	if err := r2.Flush(); err != nil {
		t.Fatal(err)
	}

	rows := r2.Usage(base, base.Add(48*time.Hour), "cust1")
	if len(rows) != 1 {
		t.Fatalf("want one row, got %+v", rows)
	}
	u := rows[0]
	if u.OperatorAccepted != 4 || u.OperatorRejected != 1 || u.OperatorDifficulty != 400 {
		t.Errorf("operator side wrong: %+v", u)
	}
	if u.CustomerAccepted != 10 || u.CustomerDifficulty != 1000 {
		t.Errorf("customer side wrong: %+v", u)
	}
	if u.Windows != 1 {
		t.Errorf("the same window was counted %d times", u.Windows)
	}
	wantSecs := int64(time.Duration(0.05*float64(15*time.Minute)).Round(time.Second) / time.Second)
	if u.AllocatedSeconds != wantSecs {
		t.Errorf("allocated %d s, want %d", u.AllocatedSeconds, wantSecs)
	}
	if u.RuleID != "rule-1" {
		t.Errorf("rule id %q", u.RuleID)
	}
	if share := u.OperatorShareOfWork(); math.Abs(share-400.0/1400.0) > 1e-9 {
		t.Errorf("share of work %.4f", share)
	}
	if rows := r2.Usage(base, base.Add(48*time.Hour), "someone-else"); len(rows) != 0 {
		t.Errorf("another customer must see nothing: %+v", rows)
	}
	if _, err := os.Stat(filepath.Join(dir, at.UTC().Format("2006-01-02")+".json")); err != nil {
		t.Errorf("day file missing: %v", err)
	}
}

func TestUsageIncludesUnflushed(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, true)
	r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Accepted: true, Difficulty: 7, At: base.Add(time.Hour)})
	rows := r.Usage(base, base.Add(24*time.Hour), "")
	if len(rows) != 1 || rows[0].CustomerAccepted != 1 {
		t.Fatalf("pending counts are missing from the report: %+v", rows)
	}
}

func TestFailedFlushKeepsCountsAndWritesThemOnce(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, dir := newRouter(t, rules, c, true)
	bad := filepath.Join(dir, "blocked")
	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.dir = filepath.Join(bad, "sub") // a path under a file cannot be created
	r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Accepted: true, Difficulty: 5, At: base.Add(time.Hour)})
	if err := r.Flush(); err == nil {
		t.Fatal("flush into an impossible directory should fail")
	}
	r.dir = dir
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	rows := r.Usage(base, base.Add(24*time.Hour), "cust1")
	if len(rows) != 1 || rows[0].CustomerAccepted != 1 || rows[0].CustomerDifficulty != 5 {
		t.Fatalf("counts lost or doubled after a failed flush: %+v", rows)
	}
}

func TestBreakerSwitchesAPoolOff(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, true)
	now := base.Add(time.Hour)
	r.now = func() time.Time { return now }
	tripped := make(chan string, 1)
	r.OnTrip = func(pool, reason string) { tripped <- pool + ": " + reason }

	rejected := func() {
		r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Operator: true, Window: 1, Accepted: false, Reason: "Unauthorized worker", At: now})
	}
	for i := 0; i < tripAfter-1; i++ {
		rejected()
	}
	if !r.Eligible("p", "dev1", "cust1") {
		t.Fatal("tripped too early")
	}
	// An accepted share resets the count.
	r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Operator: true, Window: 1, Accepted: true, Difficulty: 1, At: now})
	for i := 0; i < tripAfter-1; i++ {
		rejected()
	}
	if !r.Eligible("p", "dev1", "cust1") {
		t.Fatal("an accepted share should have reset the count")
	}
	rejected()
	if r.Eligible("p", "dev1", "cust1") {
		t.Fatal("the pool should be switched off after repeated rejections")
	}
	select {
	case <-tripped:
	case <-time.After(time.Second):
		t.Fatal("OnTrip was not called")
	}
	if len(r.Pools()) != 1 {
		t.Fatalf("status should list the pool: %+v", r.Pools())
	}
	// Another pool is not affected.
	if !r.Eligible("other", "dev1", "cust1") {
		t.Fatal("one pool's trip must not switch another off")
	}
	now = now.Add(tripCooldown + time.Second)
	if !r.Eligible("p", "dev1", "cust1") {
		t.Fatal("the pool should be tried again after the cooldown")
	}
}

func TestOperatorRefusedDoesNotExtendCooldown(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, true)
	now := base.Add(time.Hour)
	r.now = func() time.Time { return now }
	r.OperatorRefused("p", "not allowed")
	if r.Eligible("p", "dev1", "cust1") {
		t.Fatal("a refused operator worker should switch the pool off")
	}
	until := r.Pools()[0].Until
	now = now.Add(10 * time.Minute)
	r.OperatorRefused("p", "not allowed")
	if !r.Pools()[0].Until.Equal(until) {
		t.Fatal("every new session must not push the cooldown further")
	}
}

func TestConcurrentNotesAreSafe(t *testing.T) {
	rules := &fixedRules{f: 0.05}
	c := &consents{m: map[string]bool{"cust1": true}}
	r, _ := newRouter(t, rules, c, true)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.Route("p", "dev1", "cust1", base.Add(time.Hour+time.Duration(i)*time.Second))
				r.Note(Outcome{DeviceID: "dev1", CustomerID: "cust1", PoolID: "p", Accepted: true, Difficulty: 1, At: base.Add(time.Hour)})
				if i%100 == 0 {
					_ = r.Flush()
				}
			}
		}()
	}
	wg.Wait()
	if err := r.Flush(); err != nil {
		t.Fatal(err)
	}
	rows := r.Usage(base, base.Add(24*time.Hour), "cust1")
	if len(rows) != 1 || rows[0].CustomerAccepted != 4000 {
		t.Fatalf("lost or doubled counts under concurrency: %+v", rows)
	}
}

func TestFlatRulesFollowTheSettingsLive(t *testing.T) {
	f := 0.02
	r := FlatRules{Fraction: func() float64 { return f }}
	if d := r.Share(Subject{CustomerID: "c"}, time.Now()); d.Fraction != 0.02 || d.RuleID != "global" {
		t.Fatalf("got %+v", d)
	}
	f = 0
	if d := r.Share(Subject{}, time.Now()); d.Fraction != 0 {
		t.Fatalf("a zero setting must give no share, got %+v", d)
	}
	if d := (FlatRules{}).Share(Subject{}, time.Now()); d.Fraction != 0 {
		t.Fatalf("no source must give no share, got %+v", d)
	}
}

type fakeOverrides map[Subject]Override

func (f fakeOverrides) Override(s Subject) (Override, bool) {
	// The device's rule comes before the customer's, as in the store.
	if o, ok := f[Subject{CustomerID: s.CustomerID, DeviceID: s.DeviceID}]; ok {
		return o, true
	}
	o, ok := f[Subject{CustomerID: s.CustomerID}]
	return o, ok
}

func TestScopedRulesMostSpecificWins(t *testing.T) {
	base := FlatRules{Fraction: func() float64 { return 0.05 }}
	r := ScopedRules{Base: base, Overrides: fakeOverrides{
		{CustomerID: "c1"}:                 {Fraction: 0.02, Level: "customer"},
		{CustomerID: "c1", DeviceID: "d2"}: {Fraction: 0, Level: "device"},
		{CustomerID: "c2", DeviceID: "d9"}: {Fraction: 0.08, Level: "device"},
	}}
	now := time.Now()
	for _, tc := range []struct {
		sub  Subject
		frac float64
		rule string
	}{
		{Subject{CustomerID: "c1", DeviceID: "d1"}, 0.02, "customer"}, // the customer's rule
		{Subject{CustomerID: "c1", DeviceID: "d2"}, 0, "device"},      // a device left out
		{Subject{CustomerID: "c2", DeviceID: "d9"}, 0.08, "device"},
		{Subject{CustomerID: "c2", DeviceID: "d8"}, 0.05, "global"}, // nothing set: the setting
	} {
		d := r.Share(tc.sub, now)
		if d.Fraction != tc.frac || d.RuleID != tc.rule {
			t.Errorf("%+v: got %+v, want %v from %s", tc.sub, d, tc.frac, tc.rule)
		}
	}
}

func TestScopedRulesWithoutOverridesOrBase(t *testing.T) {
	if d := (ScopedRules{}).Share(Subject{}, time.Now()); d.Fraction != 0 {
		t.Fatalf("got %+v", d)
	}
	base := FlatRules{Fraction: func() float64 { return 0.03 }}
	if d := (ScopedRules{Base: base}).Share(Subject{}, time.Now()); d.Fraction != 0.03 {
		t.Fatalf("got %+v", d)
	}
}
