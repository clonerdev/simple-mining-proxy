package devices

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/session"
)

type gateEnv struct {
	t   *testing.T
	s   *Store
	g   *Gate
	clk *fakeClock
	l   *evLog
	c   Customer
	key string

	mu      sync.Mutex
	pending int
	liveFn  func(customerID, deviceID string) int
}

func newGateOn(t *testing.T, s *Store, clk *fakeClock) *gateEnv {
	t.Helper()
	e := &gateEnv{t: t, s: s, clk: clk, pending: 10}
	e.l = logEvents(s)
	e.g = NewGate(GateConfig{
		Store: s,
		PendingLimit: func() int {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.pending
		},
		Live: func(cid, did string) int {
			e.mu.Lock()
			fn := e.liveFn
			e.mu.Unlock()
			if fn == nil {
				return 0
			}
			return fn(cid, did)
		},
	})
	e.g.now = clk.Now
	return e
}

func newGateEnv(t *testing.T, in NewCustomer) *gateEnv {
	t.Helper()
	s, clk := newStore(t)
	e := newGateOn(t, s, clk)
	e.c, e.key = mkCust(t, s, in)
	return e
}

func (e *gateEnv) auth(login, pw, ip string) session.AuthDecision {
	return e.g.Authorize(session.AuthRequest{IP: ip, Login: login, Password: pw, UserAgent: "ua/1", Transport: "tcp"})
}

func (e *gateEnv) ok(worker string) session.AuthDecision {
	return e.auth("acme."+worker, e.key, "10.0.0.1")
}

func (e *gateEnv) setLive(fn func(string, string) int) {
	e.mu.Lock()
	e.liveFn = fn
	e.mu.Unlock()
}

func (e *gateEnv) setPending(n int) {
	e.mu.Lock()
	e.pending = n
	e.mu.Unlock()
}

func (e *gateEnv) suspicious(reason string) []Event {
	var out []Event
	for _, ev := range e.l.byType(EvDeviceSuspicious) {
		if ev.Reason == reason {
			out = append(out, ev)
		}
	}
	return out
}

func wantRefused(t *testing.T, d session.AuthDecision, code string) {
	t.Helper()
	if d.Allow || d.Code != code {
		t.Fatalf("decision = %+v, want refusal %q", d, code)
	}
	if want, ok := refusals[code]; !ok || d.Reason != want || d.Reason == "" {
		t.Fatalf("reason %q for code %q, want %q", d.Reason, code, want)
	}
}

// ---- 6. Authorize ----

func TestAuthorizeUnknownCustomerAndWrongKeyLookTheSame(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	cases := []struct{ name, login, pw string }{
		{"unknown customer", "nobody.rig1", e.key},
		{"unknown customer wrong key", "nobody.rig1", "wrong"},
		{"wrong key", "acme.rig1", "wrong"},
		{"empty key", "acme.rig1", ""},
		{"key of another customer", "acme.rig1", NewKey()},
		{"old key case changed", "acme.rig1", strings.ToUpper(e.key)},
		{"unknown customer, invalid worker", "nobody.bad worker", "x"},
		{"wrong key, invalid worker", "acme.bad worker", "x"},
		{"empty login", "", ""},
	}
	var first session.AuthDecision
	for i, c := range cases {
		d := e.auth(c.login, c.pw, "10.9.9.9")
		wantRefused(t, d, "unauthorized")
		if d.CustomerID != "" || d.DeviceID != "" || d.Account != "" || d.Worker != "" {
			t.Errorf("%s leaks ids: %+v", c.name, d)
		}
		if i == 0 {
			first = d
		} else if d != first {
			t.Errorf("%s: %+v differs from %+v", c.name, d, first)
		}
	}
	if n := len(e.s.Devices(Filter{})); n != 0 {
		t.Errorf("%d device records were created by failed logins", n)
	}
	if n := len(reopen(t, e.s).Devices(Filter{})); n != 0 {
		t.Errorf("%d device records on disk", n)
	}
	if n := len(e.l.byType(EvDevicePending)); n != 0 {
		t.Errorf("%d pending events", n)
	}
}

func TestAuthorizePendingThenApproved(t *testing.T) {
	e := newGateEnv(t, NewCustomer{Account: "pooluser", MaxConnections: 0})
	d := e.auth("ACME.Rack1.S21-01", e.key, "4.4.4.4")
	wantRefused(t, d, "device_pending")
	if d.CustomerID != e.c.ID || d.DeviceID == "" {
		t.Fatalf("decision = %+v", d)
	}
	if d.Account != "" || d.Worker != "" {
		t.Errorf("a refused login carries pool details: %+v", d)
	}
	dev, ok := e.s.Device(d.DeviceID)
	if !ok || dev.Status != Pending || dev.Worker != "Rack1.S21-01" || dev.Denied != 1 || dev.LastIP != "4.4.4.4" || dev.UserAgent != "ua/1" {
		t.Fatalf("device = %+v", dev)
	}
	if n := len(e.l.byType(EvDevicePending)); n != 1 {
		t.Errorf("%d pending events", n)
	}
	// Again: still pending, same device, no second event, counter goes up.
	d2 := e.auth("acme.rack1.s21-01", e.key, "4.4.4.4")
	wantRefused(t, d2, "device_pending")
	if d2.DeviceID != d.DeviceID {
		t.Errorf("a different device %q", d2.DeviceID)
	}
	if n := len(e.l.byType(EvDevicePending)); n != 1 {
		t.Errorf("%d pending events after retry", n)
	}
	if dev, _ := e.s.Device(d.DeviceID); dev.Denied != 2 {
		t.Errorf("denied = %d", dev.Denied)
	}

	if _, err := e.s.Act(d.DeviceID, ActApprove, "ok", admin); err != nil {
		t.Fatal(err)
	}
	// The ASIC's password is not needed from now on by whoever uses the
	// decision: it carries the pool account and upstream worker, not the key.
	a := e.auth("acme.Rack1.S21-01", e.key, "4.4.4.4")
	if !a.Allow || a.Code != "approved" || a.CustomerID != e.c.ID || a.DeviceID != d.DeviceID {
		t.Fatalf("decision = %+v", a)
	}
	if a.Account != "pooluser" || a.Worker != dev.UpstreamWorker || a.Worker != "rack1s2101" {
		t.Errorf("account=%q worker=%q", a.Account, a.Worker)
	}
	if strings.Contains(fmt.Sprintf("%+v", a), e.key) {
		t.Error("the decision contains the connection key")
	}
	if a.Reason != "" {
		t.Errorf("reason on allow: %q", a.Reason)
	}
	dev, _ = e.s.Device(d.DeviceID)
	if dev.Denied != 2 {
		t.Errorf("an allowed login counted as denied: %d", dev.Denied)
	}
	if !dev.LastSeen.Equal(e.clk.Now()) {
		t.Errorf("LastSeen = %v", dev.LastSeen)
	}
	// The same key still works with different case of the worker, same device.
	if b := e.auth("acme.RACK1.s21-01", e.key, "4.4.4.4"); !b.Allow || b.DeviceID != d.DeviceID {
		t.Errorf("case variant = %+v", b)
	}
	// Rotating the key stops the next login at once.
	nk, _ := e.s.RotateKey(e.c.ID, admin)
	wantRefused(t, e.auth("acme.Rack1.S21-01", e.key, "4.4.4.4"), "unauthorized")
	if a := e.auth("acme.Rack1.S21-01", nk, "4.4.4.4"); !a.Allow {
		t.Errorf("new key: %+v", a)
	}
}

func TestAuthorizeAllowCarriesDeviceMaxConnections(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	d := mkDevice(t, e.s, e.c, "rig", Approved)
	n := 3
	e.s.UpdateDevice(d.ID, DeviceUpdate{MaxConnections: &n}, admin)
	if a := e.ok("rig"); !a.Allow || a.MaxConnections != 3 {
		t.Errorf("decision = %+v", a)
	}
}

func TestAuthorizeDefaultWorker(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	d := e.auth("acme", e.key, "1.1.1.1")
	wantRefused(t, d, "device_pending")
	dev, _ := e.s.Device(d.DeviceID)
	if dev.Worker != "default" {
		t.Errorf("worker = %q", dev.Worker)
	}
	d = e.auth("  acme.  ", e.key, "1.1.1.1")
	if d.DeviceID != dev.ID {
		t.Errorf("trailing dot is another device: %+v", d)
	}
}

func TestAuthorizeDeviceStateCodes(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	cases := []struct {
		st   DeviceStatus
		code string
	}{
		{Pending, "device_pending"}, {Rejected, "device_rejected"}, {Suspended, "device_suspended"},
		{Blocked, "device_blocked"}, {Revoked, "device_revoked"}, {Approved, "approved"},
	}
	for _, c := range cases {
		d := mkDevice(t, e.s, e.c, "w-"+string(c.st), c.st)
		got := e.ok("W-" + strings.ToUpper(string(c.st)))
		if c.st == Approved {
			if !got.Allow || got.Code != c.code {
				t.Errorf("approved: %+v", got)
			}
			continue
		}
		wantRefused(t, got, c.code)
		if got.DeviceID != d.ID || got.CustomerID != e.c.ID {
			t.Errorf("%s: ids %+v", c.st, got)
		}
		if after, _ := e.s.Device(d.ID); after.Denied != 1 {
			t.Errorf("%s: denied = %d", c.st, after.Denied)
		}
	}
	if n := len(e.s.Devices(Filter{})); n != len(cases) {
		t.Errorf("%d devices, want %d (case variants must not create records)", n, len(cases))
	}
}

func TestAuthorizeDeviceExpired(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	d := mkDevice(t, e.s, e.c, "rig", Approved)
	exp := e.clk.Now().Add(time.Hour)
	e.s.UpdateDevice(d.ID, DeviceUpdate{ExpiresAt: &exp}, admin)
	if a := e.ok("rig"); !a.Allow {
		t.Fatalf("before expiry: %+v", a)
	}
	e.clk.Advance(time.Hour - time.Second)
	if a := e.ok("rig"); !a.Allow {
		t.Fatalf("a second before expiry: %+v", a)
	}
	e.clk.Advance(time.Second)
	wantRefused(t, e.ok("rig"), "device_expired")
	// Extending it opens the door again.
	later := e.clk.Now().Add(time.Hour)
	e.s.UpdateDevice(d.ID, DeviceUpdate{ExpiresAt: &later}, admin)
	if a := e.ok("rig"); !a.Allow {
		t.Errorf("after extension: %+v", a)
	}
}

func TestAuthorizeCustomerStates(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	known := mkDevice(t, e.s, e.c, "known", Approved)
	check := func(code string) {
		t.Helper()
		before := len(e.s.Devices(Filter{}))
		wantRefused(t, e.ok("known"), code)
		wantRefused(t, e.ok("brandnew"), code)
		if n := len(e.s.Devices(Filter{})); n != before {
			t.Errorf("%s: a device record was made for a customer that cannot mine", code)
		}
		if ok, why := e.s.Usable(known.ID); ok || why != code {
			t.Errorf("Usable = %v %q, want %q", ok, why, code)
		}
	}
	e.s.SetCustomerStatus(e.c.ID, CustomerSuspended, "", admin)
	check("customer_suspended")
	e.s.SetCustomerStatus(e.c.ID, CustomerBlocked, "", admin)
	check("customer_blocked")
	e.s.SetCustomerStatus(e.c.ID, CustomerActive, "", admin)
	if a := e.ok("known"); !a.Allow {
		t.Fatalf("active again: %+v", a)
	}

	// Expiry, with the boundary.
	exp := e.clk.Now().Add(time.Hour)
	e.s.UpdateCustomer(e.c.ID, CustomerUpdate{ExpiresAt: &exp}, admin)
	if a := e.ok("known"); !a.Allow {
		t.Fatalf("before expiry: %+v", a)
	}
	e.clk.Advance(time.Hour)
	check("customer_expired")
	// A blocked customer says blocked, not expired.
	e.s.SetCustomerStatus(e.c.ID, CustomerBlocked, "", admin)
	check("customer_blocked")
	e.s.SetCustomerStatus(e.c.ID, CustomerActive, "", admin)
	e.s.UpdateCustomer(e.c.ID, CustomerUpdate{ClearExpires: true}, admin)
	if a := e.ok("known"); !a.Allow {
		t.Fatalf("expiry cleared: %+v", a)
	}
}

func TestAuthorizeConnectionLimit(t *testing.T) {
	e := newGateEnv(t, NewCustomer{MaxConnections: 2})
	d := mkDevice(t, e.s, e.c, "rig", Approved)
	var gotC, gotD string
	live := 2
	e.setLive(func(cid, did string) int { gotC, gotD = cid, did; return live })

	got := e.ok("rig")
	wantRefused(t, got, "connection_limit")
	if gotC != e.c.ID || gotD != d.ID {
		t.Errorf("Live called with %q, %q", gotC, gotD)
	}
	if got.DeviceID != d.ID {
		t.Errorf("decision = %+v", got)
	}
	if dev, _ := e.s.Device(d.ID); dev.Denied != 1 {
		t.Errorf("denied = %d", dev.Denied)
	}
	// Deduplicated alert.
	e.ok("rig")
	e.ok("rig")
	if n := len(e.suspicious("connection_limit")); n != 1 {
		t.Errorf("%d connection_limit alerts, want 1", n)
	}
	e.clk.Advance(suspiciousThrottle + time.Second)
	e.ok("rig")
	if n := len(e.suspicious("connection_limit")); n != 2 {
		t.Errorf("%d alerts after the window, want 2", n)
	}
	ev := e.suspicious("connection_limit")[0]
	if ev.DeviceID != d.ID || ev.CustomerID != e.c.ID || ev.Worker != "rig" {
		t.Errorf("event %+v", ev)
	}
	// Refusing for the limit never counts toward the IP lock.
	for i := 0; i < 3*failLimit; i++ {
		e.ok("rig")
	}
	if !e.g.AllowConnect("10.0.0.1") {
		t.Error("connection_limit refusals locked the address")
	}
	live = 1
	if a := e.ok("rig"); !a.Allow {
		t.Errorf("under the limit: %+v", a)
	}
	// No customer cap: Live is not consulted.
	e.s.UpdateCustomer(e.c.ID, CustomerUpdate{MaxConnections: new(int)}, admin)
	live = 1000
	if a := e.ok("rig"); !a.Allow {
		t.Errorf("no cap: %+v", a)
	}
}

func TestAuthorizeInvalidWorker(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	for _, w := range []string{"bad worker", "a%b", "a'b", "-x", strings.Repeat("a", 65), "نام"} {
		d := e.auth("acme."+w, e.key, "1.1.1.1")
		wantRefused(t, d, "worker_invalid")
	}
	if n := len(e.s.Devices(Filter{})); n != 0 {
		t.Errorf("%d devices", n)
	}
	// With a wrong key the same login is plain unauthorized: no hint.
	wantRefused(t, e.auth("acme.bad worker", "nope", "1.1.1.1"), "unauthorized")
	// And invalid workers with the right key are not key-guessing.
	for i := 0; i < 2*failLimit; i++ {
		e.auth("acme.bad worker", e.key, "2.2.2.2")
	}
	if !e.g.AllowConnect("2.2.2.2") || e.g.ips["2.2.2.2"] != nil {
		t.Error("worker_invalid counted as a failure")
	}
}

func TestAuthorizePendingLimit(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	e.setPending(2)
	wantRefused(t, e.ok("a"), "device_pending")
	wantRefused(t, e.ok("b"), "device_pending")
	d := e.ok("c")
	wantRefused(t, d, "pending_limit")
	if d.DeviceID != "" {
		t.Errorf("device id on pending_limit: %+v", d)
	}
	if _, ok := e.s.DeviceFor(e.c.ID, "c"); ok {
		t.Error("a record was made over the pending limit")
	}
	if n := len(e.s.Devices(Filter{})); n != 2 {
		t.Errorf("%d devices", n)
	}
	// Known pending devices are still answered as pending.
	wantRefused(t, e.ok("A"), "device_pending")
	// One alert, however often it repeats.
	for i := 0; i < 10; i++ {
		e.ok(fmt.Sprintf("x%d", i))
	}
	if n := len(e.suspicious("pending_limit")); n != 1 {
		t.Errorf("%d pending_limit alerts, want 1", n)
	}
	// Not an authentication failure.
	if !e.g.AllowConnect("10.0.0.1") || len(e.g.ips) != 0 {
		t.Error("pending_limit counted toward the IP lock")
	}
	// The setting is read on every login.
	e.setPending(0)
	wantRefused(t, e.ok("c"), "device_pending")
}

func TestAuthorizeInternalErrorWhenDeviceCapReached(t *testing.T) {
	s, clk := newStore(t)
	c, key := mkCust(t, s, NewCustomer{})
	s = seedFile(t, s, func(f *file) {
		for i := 0; i < maxPerCustomer; i++ {
			w := fmt.Sprintf("w%d", i)
			f.Devices = append(f.Devices, Device{ID: "d" + w, CustomerID: c.ID, Worker: w, UpstreamWorker: w, Status: Rejected})
		}
	})
	e := newGateOn(t, s, clk)
	d := e.auth("acme.newone", key, "1.1.1.1")
	wantRefused(t, d, "internal")
	if _, ok := s.DeviceFor(c.ID, "newone"); ok {
		t.Error("record made")
	}
	if !e.g.AllowConnect("1.1.1.1") || len(e.g.ips) != 0 {
		t.Error("an internal refusal counted toward the lock")
	}
	wantRefused(t, e.auth("acme.w7", key, "1.1.1.1"), "device_rejected")
}

func TestRefusalTexts(t *testing.T) {
	for code, text := range refusals {
		if text == "" {
			t.Errorf("no text for %s", code)
		}
		if d := refuse(code); d.Code != code || d.Reason != text || d.Allow {
			t.Errorf("refuse(%q) = %+v", code, d)
		}
	}
	if d := refuse("something_new"); d.Reason != refusals["unauthorized"] || d.Code != "something_new" {
		t.Errorf("unknown code: %+v", d)
	}
	// Every code Usable can produce has a text.
	for _, st := range []DeviceStatus{Pending, Approved, Rejected, Suspended, Blocked, Revoked} {
		if _, why := Usable(Customer{}, Device{Status: st}, time.Now()); why != "" {
			if _, ok := refusals[why]; !ok {
				t.Errorf("no refusal text for %q", why)
			}
		}
	}
	for _, why := range []string{"customer_suspended", "customer_blocked", "customer_expired", "device_expired"} {
		if _, ok := refusals[why]; !ok {
			t.Errorf("no refusal text for %q", why)
		}
	}
}

// ---- IP lock ----

func TestIPLockAfterFailLimitAndExpiry(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	mkDevice(t, e.s, e.c, "rig", Approved)
	ip := "6.6.6.6"
	for i := 0; i < failLimit-1; i++ {
		// Alternate unknown customers and wrong keys.
		login := "acme.rig"
		if i%2 == 0 {
			login = fmt.Sprintf("nobody%d.rig", i)
		}
		wantRefused(t, e.auth(login, "guess", ip), "unauthorized")
	}
	if !e.g.AllowConnect(ip) {
		t.Fatalf("locked after %d failures", failLimit-1)
	}
	wantRefused(t, e.auth("acme.rig", "guess", ip), "unauthorized") // the failLimit-th
	if e.g.AllowConnect(ip) {
		t.Fatal("not locked after failLimit failures")
	}
	if !e.g.AllowConnect("6.6.6.7") {
		t.Error("another address is locked")
	}
	// Even the right key is refused from a locked address, and makes no record.
	d := e.auth("acme.rig", e.key, ip)
	wantRefused(t, d, "throttled")
	wantRefused(t, e.auth("acme.fresh", e.key, ip), "throttled")
	if _, ok := e.s.DeviceFor(e.c.ID, "fresh"); ok {
		t.Error("a device was made for a throttled login")
	}
	if a := e.auth("acme.rig", e.key, "6.6.6.7"); !a.Allow {
		t.Errorf("other address: %+v", a)
	}
	// Retrying while locked does not extend the lock.
	e.clk.Advance(lockFor / 2)
	wantRefused(t, e.auth("acme.rig", "guess", ip), "throttled")
	e.clk.Advance(lockFor/2 - time.Second)
	if e.g.AllowConnect(ip) {
		t.Error("unlocked early")
	}
	e.clk.Advance(time.Second)
	if !e.g.AllowConnect(ip) {
		t.Error("still locked at lockFor")
	}
	if a := e.auth("acme.rig", e.key, ip); !a.Allow {
		t.Errorf("after the lock: %+v", a)
	}
	// The count started again from zero.
	for i := 0; i < failLimit-1; i++ {
		e.auth("acme.rig", "guess", ip)
	}
	if !e.g.AllowConnect(ip) {
		t.Error("the old failures still counted after the lock ended")
	}
	e.auth("acme.rig", "guess", ip)
	if e.g.AllowConnect(ip) {
		t.Error("not locked the second time")
	}
	// ip_locked alerts: one per lock event inside the window; the first one exists.
	if n := len(e.suspicious("ip_locked")); n < 1 {
		t.Error("no ip_locked alert")
	}
}

func TestIPFailuresOutsideWindowDoNotAdd(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	ip := "7.7.7.7"
	for i := 0; i < failLimit-1; i++ {
		e.auth("acme.rig", "guess", ip)
	}
	e.clk.Advance(failWindow + time.Second)
	for i := 0; i < failLimit-1; i++ {
		e.auth("acme.rig", "guess", ip)
	}
	if !e.g.AllowConnect(ip) {
		t.Error("failures older than the window were counted")
	}
	e.auth("acme.rig", "guess", ip)
	if e.g.AllowConnect(ip) {
		t.Error("not locked")
	}
}

func TestIPLockNotTriggeredByRefusalsWithTheRightKey(t *testing.T) {
	e := newGateEnv(t, NewCustomer{MaxConnections: 1})
	ip := "8.8.8.8" // a NAT address of a farm
	login := func(w string) session.AuthDecision { return e.auth("acme."+w, e.key, ip) }

	mkDevice(t, e.s, e.c, "blocked", Blocked)
	mkDevice(t, e.s, e.c, "revoked", Revoked)
	mkDevice(t, e.s, e.c, "suspended", Suspended)
	mkDevice(t, e.s, e.c, "rejected", Rejected)
	mkDevice(t, e.s, e.c, "limit", Approved)
	e.setLive(func(string, string) int { return 5 })
	e.setPending(1)
	for i := 0; i < 3*failLimit; i++ {
		wantRefused(t, login("pend"), "device_pending")
		wantRefused(t, login("blocked"), "device_blocked")
		wantRefused(t, login("revoked"), "device_revoked")
		wantRefused(t, login("suspended"), "device_suspended")
		wantRefused(t, login("rejected"), "device_rejected")
		wantRefused(t, login("limit"), "connection_limit")
		wantRefused(t, login("another new one"), "worker_invalid")
		wantRefused(t, login("second"), "pending_limit")
	}
	e.s.SetCustomerStatus(e.c.ID, CustomerSuspended, "", admin)
	for i := 0; i < failLimit; i++ {
		wantRefused(t, login("pend"), "customer_suspended")
	}
	e.s.SetCustomerStatus(e.c.ID, CustomerActive, "", admin)
	if !e.g.AllowConnect(ip) {
		t.Fatal("the NAT address was locked by logins with the right key")
	}
	if n := len(e.g.ips); n != 0 {
		t.Errorf("%d addresses tracked, want 0", n)
	}
	// Real guessing from the same address still gets it locked, and the
	// right-key refusals neither helped nor hindered.
	for i := 0; i < failLimit-1; i++ {
		e.auth("acme.pend", "guess", ip)
		login("blocked")
	}
	if !e.g.AllowConnect(ip) {
		t.Fatal("locked one failure early")
	}
	e.auth("acme.pend", "guess", ip)
	if e.g.AllowConnect(ip) {
		t.Error("not locked after failLimit real failures")
	}
}

func TestIPTrackingIsBounded(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	now := e.clk.Now()
	stale := now.Add(-2 * failWindow)
	for i := 0; i < maxTracked; i++ {
		e.g.ips[fmt.Sprintf("ip%d", i)] = &ipState{fails: []time.Time{stale}}
	}
	// Full of quiet entries: they are forgotten to make room.
	wantRefused(t, e.auth("acme.rig", "guess", "new1"), "unauthorized")
	if len(e.g.ips) >= maxTracked || e.g.ips["new1"] == nil {
		t.Errorf("stale entries not purged: %d tracked, new1=%v", len(e.g.ips), e.g.ips["new1"])
	}
	// Full of busy entries: new addresses are not tracked, nothing panics, and
	// the memory is bounded.
	e.g.ips = map[string]*ipState{}
	for i := 0; i < maxTracked; i++ {
		e.g.ips[fmt.Sprintf("ip%d", i)] = &ipState{fails: []time.Time{now}}
	}
	wantRefused(t, e.auth("acme.rig", "guess", "new2"), "unauthorized")
	if len(e.g.ips) != maxTracked || e.g.ips["new2"] != nil {
		t.Errorf("tracked %d, new2=%v", len(e.g.ips), e.g.ips["new2"])
	}
	if !e.g.AllowConnect("new2") {
		t.Error("untracked address locked")
	}
}

func TestSuspiciousEventsAreDeduplicated(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	for i := 0; i < 5; i++ {
		e.auth("acme.rig", "guess", "1.1.1.1")
	}
	evs := e.suspicious("wrong_key")
	if len(evs) != 1 {
		t.Fatalf("%d wrong_key events, want 1", len(evs))
	}
	if ev := evs[0]; ev.CustomerID != e.c.ID || ev.IP != "1.1.1.1" || ev.Worker != "rig" || ev.Actor.IP != "1.1.1.1" {
		t.Errorf("event %+v", ev)
	}
	// A different address is a different alert; so is the window ending.
	e.auth("acme.rig", "guess", "1.1.1.2")
	if n := len(e.suspicious("wrong_key")); n != 2 {
		t.Errorf("%d events for two addresses", n)
	}
	e.clk.Advance(suspiciousThrottle + time.Second)
	e.auth("acme.rig", "guess", "1.1.1.1")
	if n := len(e.suspicious("wrong_key")); n != 3 {
		t.Errorf("%d events after the window", n)
	}
	// An unknown customer raises no wrong_key alert (nothing to attach it to).
	for i := 0; i < 3; i++ {
		e.auth("ghost.rig", "guess", "1.1.1.3")
	}
	if n := len(e.suspicious("wrong_key")); n != 3 {
		t.Errorf("unknown customer raised alerts: %d", n)
	}
	// The lock alert is sent once per lock for an address.
	for i := 0; i < failLimit+5; i++ {
		e.auth("ghost.rig", "guess", "2.2.2.2")
	}
	if n := len(e.suspicious("ip_locked")); n != 1 {
		t.Errorf("%d ip_locked alerts, want 1", n)
	}
}

func TestGateOnce(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	g := e.g
	now := e.clk.Now()
	w := time.Minute
	if !g.once("k", now, w) {
		t.Fatal("first call false")
	}
	if g.once("k", now.Add(w-time.Nanosecond), w) {
		t.Error("repeat inside the window true")
	}
	if !g.once("other", now, w) {
		t.Error("another key false")
	}
	if !g.once("k", now.Add(w), w) {
		t.Error("at the end of the window false")
	}
	if g.once("k", now.Add(w+time.Second), w) {
		t.Error("window restarts at the last true")
	}
	// Bounded: when full of live keys a new one is refused (quietly), when they
	// have aged out they are purged.
	g.dedup = map[string]time.Time{}
	for i := 0; i < maxDedupe; i++ {
		g.dedup[fmt.Sprint("fill", i)] = now
	}
	if g.once("new", now, w) {
		t.Error("accepted over the bound")
	}
	if len(g.dedup) != maxDedupe {
		t.Errorf("grew to %d", len(g.dedup))
	}
	if !g.once("new", now.Add(2*w), w) {
		t.Error("not accepted after the old keys aged")
	}
	if len(g.dedup) > 1 {
		t.Errorf("aged keys not purged: %d", len(g.dedup))
	}
}

func TestGateDefaultsAndPassThroughs(t *testing.T) {
	s, _ := newStore(t)
	g := NewGate(GateConfig{Store: s})
	if !g.Active() {
		t.Error("default gate inactive")
	}
	if g.pending() != 10 || g.live("a", "b") != 0 {
		t.Error("defaults")
	}
	if !g.AllowConnect("1.2.3.4") {
		t.Error("fresh address refused")
	}
	if ok, why := g.Usable("nope"); ok || why != "device_deleted" {
		t.Errorf("Usable = %v %q", ok, why)
	}
	off := NewGate(GateConfig{Store: s, Enabled: func() bool { return false }})
	if off.Active() {
		t.Error("Enabled=false is active")
	}
	var _ session.Gate = g
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Approved)
	if ok, why := g.Usable(d.ID); !ok || why != "" {
		t.Errorf("Usable = %v %q", ok, why)
	}
	act(t, s, d.ID, ActBlock)
	if ok, why := g.Usable(d.ID); ok || why != "device_blocked" {
		t.Errorf("Usable after block = %v %q", ok, why)
	}
}

// ---- 3 and 7 through the Gate ----

func TestAuthorizeCaseChangeCannotBypassRevokedOrBlocked(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	for _, st := range []DeviceStatus{Revoked, Blocked} {
		mkDevice(t, e.s, e.c, "Rig-"+string(st), st)
		for _, w := range []string{"Rig-" + string(st), "rig-" + string(st), strings.ToUpper("rig-" + string(st))} {
			wantRefused(t, e.ok(w), "device_"+string(st))
		}
	}
	if n := len(e.s.Devices(Filter{})); n != 2 {
		t.Errorf("%d devices, want 2", n)
	}
	// The slug's case does not matter either, and still no new record.
	wantRefused(t, e.auth("ACME.RIG-REVOKED", e.key, "1.1.1.1"), "device_revoked")
	if n := len(e.s.Devices(Filter{})); n != 2 {
		t.Errorf("%d devices after slug case change", n)
	}
}

func TestAuthorizeConcurrentSameWorker(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	variants := []string{"Rig", "rig", "RIG", "rIg"}
	const n = 60
	ds := make([]session.AuthDecision, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ds[i] = e.auth("acme."+variants[i%4], e.key, fmt.Sprintf("10.1.0.%d", i%5))
			e.auth("acme."+variants[i%4], "wrong", fmt.Sprintf("10.2.0.%d", i%5)) // also hammer the limiter
			e.g.AllowConnect("10.2.0.1")
		}(i)
	}
	close(start)
	wg.Wait()
	for i, d := range ds {
		if d.Code != "device_pending" || d.DeviceID != ds[0].DeviceID || d.DeviceID == "" {
			t.Fatalf("decision %d = %+v", i, d)
		}
	}
	if got := len(e.s.Devices(Filter{})); got != 1 {
		t.Errorf("%d devices, want 1", got)
	}
	if got := len(e.l.byType(EvDevicePending)); got != 1 {
		t.Errorf("%d pending events", got)
	}
	if dev, _ := e.s.Device(ds[0].DeviceID); dev.Denied != n {
		t.Errorf("denied = %d, want %d", dev.Denied, n)
	}
}

func TestAuthorizeSiblingDevicesAreIndependent(t *testing.T) {
	for _, a := range []Action{ActSuspend, ActBlock, ActRevoke} {
		t.Run(string(a), func(t *testing.T) {
			e := newGateEnv(t, NewCustomer{MaxDevices: 2})
			da := mkDevice(t, e.s, e.c, "a", Approved)
			db := mkDevice(t, e.s, e.c, "b", Approved)
			if !e.ok("a").Allow || !e.ok("b").Allow {
				t.Fatal("both should be allowed first")
			}
			act(t, e.s, da.ID, a)
			wantRefused(t, e.ok("a"), "device_"+map[Action]string{ActSuspend: "suspended", ActBlock: "blocked", ActRevoke: "revoked"}[a])
			if ok, _ := e.g.Usable(da.ID); ok {
				t.Error("A usable")
			}
			bd := e.ok("b")
			if !bd.Allow || bd.DeviceID != db.ID {
				t.Errorf("B = %+v", bd)
			}
			if ok, why := e.g.Usable(db.ID); !ok {
				t.Errorf("B Usable: %s", why)
			}
		})
	}
}

func TestAuthorizeSameWorkerNameOfTwoCustomersIsTwoDevices(t *testing.T) {
	e := newGateEnv(t, NewCustomer{})
	c2, key2 := mkCust(t, e.s, NewCustomer{Slug: "beta", Account: "beta-acc"})
	d1 := e.ok("rig")
	d2 := e.auth("beta.rig", key2, "1.1.1.1")
	if d1.DeviceID == d2.DeviceID || d1.CustomerID != e.c.ID || d2.CustomerID != c2.ID {
		t.Errorf("%+v %+v", d1, d2)
	}
	// One customer's key does not open the other's.
	wantRefused(t, e.auth("beta.rig", e.key, "1.1.1.1"), "unauthorized")
	e.s.Act(d2.DeviceID, ActApprove, "", admin)
	a := e.auth("beta.rig", key2, "1.1.1.1")
	if !a.Allow || a.Account != "beta-acc" {
		t.Errorf("%+v", a)
	}
	wantRefused(t, e.ok("rig"), "device_pending")
}

func TestAuthorizeAfterReopenKeepsDecisions(t *testing.T) {
	e := newGateEnv(t, NewCustomer{Account: "acc"})
	d := e.ok("rig")
	e.s.Act(d.DeviceID, ActApprove, "", admin)
	e.s.Flush()
	s2 := reopen(t, e.s)
	e2 := newGateOn(t, s2, e.clk)
	a := e2.auth("acme.RIG", e.key, "1.1.1.1")
	if !a.Allow || a.Account != "acc" || a.DeviceID != d.DeviceID {
		t.Errorf("after reopen: %+v", a)
	}
	// Unknown customer after reopen is still the generic refusal, no record.
	wantRefused(t, e2.auth("ghost.rig", "x", "1.1.1.1"), "unauthorized")
	if n := len(s2.Devices(Filter{})); n != 1 {
		t.Errorf("%d devices", n)
	}
}
