package devices

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
)

// ---- helpers ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var admin = audit.Actor{Type: audit.ActorUser, Name: "admin"}

func newStore(t *testing.T) (*Store, *fakeClock) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	clk := newClock()
	s.now = clk.Now
	return s, clk
}

type evLog struct {
	mu  sync.Mutex
	evs []Event
}

func (l *evLog) add(e Event) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}

func (l *evLog) all() []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Event(nil), l.evs...)
}

func (l *evLog) byType(typ string) []Event {
	var out []Event
	for _, e := range l.all() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func logEvents(s *Store) *evLog {
	l := &evLog{}
	s.OnEvent(l.add)
	return l
}

func mkCust(t *testing.T, s *Store, in NewCustomer) (Customer, string) {
	t.Helper()
	if in.Slug == "" {
		in.Slug = "acme"
	}
	if in.Name == "" {
		in.Name = "Acme"
	}
	c, key, err := s.CreateCustomer(in, admin)
	if err != nil {
		t.Fatalf("CreateCustomer: %v", err)
	}
	return c, key
}

func register(t *testing.T, s *Store, c Customer, worker string) Device {
	t.Helper()
	d, _, err := s.Register(c, worker, "ua", "10.0.0.1", 0)
	if err != nil {
		t.Fatalf("Register(%q): %v", worker, err)
	}
	return d
}

func act(t *testing.T, s *Store, id string, a Action) Device {
	t.Helper()
	d, err := s.Act(id, a, "", admin)
	if err != nil {
		t.Fatalf("Act(%s): %v", a, err)
	}
	return d
}

// mkDevice registers a worker and walks it through valid actions to status.
// A blocked device was approved first, so unblock restores Approved.
func mkDevice(t *testing.T, s *Store, c Customer, worker string, status DeviceStatus) Device {
	t.Helper()
	d := register(t, s, c, worker)
	switch status {
	case Pending:
	case Approved:
		act(t, s, d.ID, ActApprove)
	case Rejected:
		act(t, s, d.ID, ActReject)
	case Suspended:
		act(t, s, d.ID, ActApprove)
		act(t, s, d.ID, ActSuspend)
	case Blocked:
		act(t, s, d.ID, ActApprove)
		act(t, s, d.ID, ActBlock)
	case Revoked:
		act(t, s, d.ID, ActApprove)
		act(t, s, d.ID, ActRevoke)
	default:
		t.Fatalf("bad status %q", status)
	}
	got, ok := s.Device(d.ID)
	if !ok || got.Status != status {
		t.Fatalf("mkDevice: status %q, want %q", got.Status, status)
	}
	return got
}

func errKey(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Key
	}
	return ""
}

func wantKey(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %q, got nil", key)
	}
	if got := errKey(err); got != key {
		t.Fatalf("want error key %q, got %q (%v)", key, got, err)
	}
	if _, ok := Messages[key]; !ok {
		t.Errorf("error key %q has no entry in Messages", key)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tp(t time.Time) *time.Time { return &t }

func reopen(t *testing.T, s *Store) *Store {
	t.Helper()
	s2, err := Open(s.doc.Path())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2.now = s.now
	return s2
}

// tamper edits the file the store holds, the way a hand-edited or older file
// would look, and rebuilds the indexes.
func tamper(t *testing.T, s *Store, fn func(f *file)) {
	t.Helper()
	if err := s.doc.Mutate(func(f *file) error { fn(f); return nil }, s.reindex); err != nil {
		t.Fatal(err)
	}
}

// ---- 1. state machine ----

func TestStateMachineEveryActionFromEveryStatus(t *testing.T) {
	// Written out by hand, not read from the code under test. Missing = denied.
	// A Blocked device in this test was approved before it was blocked.
	allowed := map[DeviceStatus]map[Action]DeviceStatus{
		Pending:   {ActApprove: Approved, ActReject: Rejected, ActBlock: Blocked},
		Approved:  {ActSuspend: Suspended, ActBlock: Blocked, ActRevoke: Revoked},
		Rejected:  {ActReopen: Pending, ActBlock: Blocked},
		Suspended: {ActResume: Approved, ActBlock: Blocked, ActRevoke: Revoked},
		Blocked:   {ActUnblock: Approved, ActRevoke: Revoked},
		Revoked:   {},
	}
	statuses := []DeviceStatus{Pending, Approved, Rejected, Suspended, Blocked, Revoked}
	if len(Actions) != 8 {
		t.Fatalf("Actions has %d entries, test knows 8", len(Actions))
	}
	for _, from := range statuses {
		for _, a := range Actions {
			t.Run(string(from)+"/"+string(a), func(t *testing.T) {
				s, _ := newStore(t)
				c, _ := mkCust(t, s, NewCustomer{})
				d := mkDevice(t, s, c, "rig1", from)
				l := logEvents(s)
				got, err := s.Act(d.ID, a, "because", admin)
				to, ok := allowed[from][a]
				if !ok {
					wantKey(t, err, "transition_denied")
					var e *Error
					errors.As(err, &e)
					if e.Params["from"] != string(from) || e.Params["action"] != string(a) {
						t.Errorf("params = %v", e.Params)
					}
					after, _ := s.Device(d.ID)
					if jsonOf(t, after) != jsonOf(t, d) {
						t.Errorf("denied action changed the device:\n%s\n%s", jsonOf(t, d), jsonOf(t, after))
					}
					if n := len(l.all()); n != 0 {
						t.Errorf("denied action emitted %d events", n)
					}
					return
				}
				if err != nil {
					t.Fatalf("Act: %v", err)
				}
				if got.Status != to {
					t.Fatalf("status %q, want %q", got.Status, to)
				}
				stored, _ := s.Device(d.ID)
				if stored.Status != to || stored.StatusReason != "because" || stored.StatusBy != "admin" {
					t.Errorf("stored = %+v", stored)
				}
				if len(stored.History) != len(d.History)+1 {
					t.Fatalf("history %d -> %d", len(d.History), len(stored.History))
				}
				last := stored.History[len(stored.History)-1]
				if last.From != from || last.To != to || last.By != "admin" || last.Reason != "because" {
					t.Errorf("history entry = %+v", last)
				}
				switch a {
				case ActBlock:
					if stored.SavedStatus != from {
						t.Errorf("SavedStatus = %q, want %q", stored.SavedStatus, from)
					}
				case ActUnblock:
					if stored.SavedStatus != "" {
						t.Errorf("SavedStatus after unblock = %q", stored.SavedStatus)
					}
				}
				evs := l.all()
				if len(evs) != 1 || evs[0].From != string(from) || evs[0].To != string(to) {
					t.Errorf("events = %+v", evs)
				}
			})
		}
	}
}

func TestUnknownActionAndDevice(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig1")
	_, err := s.Act(d.ID, Action("explode"), "", admin)
	wantKey(t, err, "action_invalid")
	_, err = s.Act("nope", Action("explode"), "", admin)
	wantKey(t, err, "action_invalid")
	_, err = s.Act("nope", ActApprove, "", admin)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnblockRestoresSavedStatus(t *testing.T) {
	for _, origin := range []DeviceStatus{Pending, Approved, Rejected, Suspended} {
		t.Run(string(origin), func(t *testing.T) {
			s, _ := newStore(t)
			c, _ := mkCust(t, s, NewCustomer{})
			d := mkDevice(t, s, c, "rig1", origin)
			b := act(t, s, d.ID, ActBlock)
			if b.Status != Blocked || b.SavedStatus != origin {
				t.Fatalf("blocked = %+v", b)
			}
			u := act(t, s, d.ID, ActUnblock)
			if u.Status != origin || u.SavedStatus != "" {
				t.Fatalf("unblocked = %+v, want %q", u, origin)
			}
		})
	}
}

func TestUnblockFallsBackToPending(t *testing.T) {
	for _, saved := range []DeviceStatus{"", Blocked, "bogus"} {
		t.Run("saved="+string(saved), func(t *testing.T) {
			s, _ := newStore(t)
			c, _ := mkCust(t, s, NewCustomer{})
			d := mkDevice(t, s, c, "rig1", Blocked)
			tamper(t, s, func(f *file) { f.Devices[0].SavedStatus = saved })
			u := act(t, s, d.ID, ActUnblock)
			if u.Status != Pending {
				t.Fatalf("status = %q, want pending", u.Status)
			}
		})
	}
}

func TestHistoryCappedAtMaxHistory(t *testing.T) {
	s, clk := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig1")
	act(t, s, d.ID, ActApprove) // change 1
	for i := 0; i < 29; i++ {   // changes 2..30
		clk.Advance(time.Minute)
		if i%2 == 0 {
			act(t, s, d.ID, ActSuspend)
		} else {
			act(t, s, d.ID, ActResume)
		}
	}
	got, _ := s.Device(d.ID)
	if len(got.History) != maxHistory {
		t.Fatalf("history len = %d, want %d", len(got.History), maxHistory)
	}
	// The newest entry is kept, the oldest 10 were dropped.
	last := got.History[len(got.History)-1]
	if last.To != got.Status {
		t.Errorf("last.To = %q, status = %q", last.To, got.Status)
	}
	for i := 1; i < len(got.History); i++ {
		if got.History[i].From != got.History[i-1].To {
			t.Errorf("chain broken at %d: %+v -> %+v", i, got.History[i-1], got.History[i])
		}
		if !got.History[i].At.After(got.History[i-1].At) {
			t.Errorf("history not in time order at %d", i)
		}
	}
	// Change 11 happened after 10 one-minute steps after change 1... change k
	// (k>=2) is at +(k-1) minutes, so the first kept (k=11) is at +10 minutes.
	first := got.History[0]
	start := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if want := start.Add(10 * time.Minute); !first.At.Equal(want) {
		t.Errorf("first kept entry at %v, want %v", first.At, want)
	}
	if got.History[0].From != Approved && got.History[0].From != Suspended {
		t.Errorf("first kept From = %q", got.History[0].From)
	}
}

func TestReasonLimitAndTrim(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig1")

	_, err := s.Act(d.ID, ActApprove, strings.Repeat("x", maxReason+1), admin)
	wantKey(t, err, "reason_long")
	if got, _ := s.Device(d.ID); got.Status != Pending || len(got.History) != 0 {
		t.Fatalf("refused reason changed the device: %+v", got)
	}

	// Limit is in characters, not bytes: 200 Persian letters are 400 bytes.
	fa := strings.Repeat("ن", maxReason)
	if _, err := s.Act(d.ID, ActApprove, fa, admin); err != nil {
		t.Fatalf("200-rune reason refused: %v", err)
	}
	_, err = s.Act(d.ID, ActSuspend, fa+"ن", admin)
	wantKey(t, err, "reason_long")

	// Trimmed first: spaces around a 200 char reason do not count.
	if _, err := s.Act(d.ID, ActSuspend, "  "+strings.Repeat("y", maxReason)+"  ", admin); err != nil {
		t.Fatalf("padded reason: %v", err)
	}
	got, _ := s.Device(d.ID)
	if got.StatusReason != strings.Repeat("y", maxReason) {
		t.Errorf("reason not trimmed: %q", got.StatusReason)
	}
}

func TestActorNameFallsBackToType(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig1")
	got, err := s.Act(d.ID, ActApprove, "", audit.Actor{Type: audit.ActorTelegram})
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusBy != audit.ActorTelegram || got.History[0].By != audit.ActorTelegram {
		t.Errorf("by = %q / %q", got.StatusBy, got.History[0].By)
	}
}

// ---- 2. limits ----

func TestMaxDevicesCountsApprovedAndSuspendedOnly(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{MaxDevices: 2})
	a := mkDevice(t, s, c, "a", Pending)
	b := mkDevice(t, s, c, "b", Pending)
	d := mkDevice(t, s, c, "c", Pending)
	e := mkDevice(t, s, c, "d", Pending)
	// Pending never holds a slot: all four may wait.
	act(t, s, a.ID, ActApprove)
	act(t, s, b.ID, ActApprove)
	_, err := s.Act(d.ID, ActApprove, "", admin)
	wantKey(t, err, "device_limit")
	if got, _ := s.Device(d.ID); got.Status != Pending {
		t.Errorf("refused approve changed status to %q", got.Status)
	}

	// Suspended still holds its slot.
	act(t, s, a.ID, ActSuspend)
	_, err = s.Act(d.ID, ActApprove, "", admin)
	wantKey(t, err, "device_limit")

	// Rejected, blocked and revoked do not hold one.
	act(t, s, a.ID, ActRevoke)
	act(t, s, d.ID, ActApprove) // takes the freed slot
	_, err = s.Act(e.ID, ActApprove, "", admin)
	wantKey(t, err, "device_limit")
	act(t, s, b.ID, ActBlock) // frees one
	act(t, s, e.ID, ActApprove)

	// Unblocking b back to Approved needs a slot and there is none.
	_, err = s.Act(b.ID, ActUnblock, "", admin)
	wantKey(t, err, "device_limit")
	if got, _ := s.Device(b.ID); got.Status != Blocked || got.SavedStatus != Approved {
		t.Errorf("failed unblock changed device: %+v", got)
	}
	act(t, s, e.ID, ActBlock)
	act(t, s, b.ID, ActUnblock)

	// Counts of a different customer do not matter.
	c2, _ := mkCust(t, s, NewCustomer{Slug: "other"})
	x := mkDevice(t, s, c2, "a", Pending)
	act(t, s, x.ID, ActApprove)
}

func TestMaxDevicesZeroIsNoCap(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{MaxDevices: 0})
	for i := 0; i < 15; i++ {
		mkDevice(t, s, c, fmt.Sprintf("w%d", i), Approved)
	}
}

func TestResumeNeedsNoExtraSlot(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{MaxDevices: 2})
	a := mkDevice(t, s, c, "a", Approved)
	b := mkDevice(t, s, c, "b", Approved)
	act(t, s, a.ID, ActSuspend)
	act(t, s, a.ID, ActResume) // full, but a keeps its own slot

	// Even with the cap lowered below the number of slots held, resume works
	// and suspend/resume never fail on the limit.
	act(t, s, a.ID, ActSuspend)
	one := 1
	if _, err := s.UpdateCustomer(c.ID, CustomerUpdate{MaxDevices: &one}, admin); err != nil {
		t.Fatal(err)
	}
	if got := act(t, s, a.ID, ActResume); got.Status != Approved {
		t.Fatalf("resume = %q", got.Status)
	}
	// But a third device cannot be approved.
	c3 := mkDevice(t, s, c, "c", Pending)
	_, err := s.Act(c3.ID, ActApprove, "", admin)
	wantKey(t, err, "device_limit")
	_ = b
}

func TestRegisterPendingLimit(t *testing.T) {
	s, _ := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	reg := func(w string, limit int) (Device, bool, error) {
		return s.Register(c, w, "ua", "1.2.3.4", limit)
	}
	for _, w := range []string{"a", "b"} {
		if _, created, err := reg(w, 2); err != nil || !created {
			t.Fatalf("register %s: created=%v err=%v", w, created, err)
		}
	}
	before := jsonOf(t, s.Devices(Filter{}))
	nEvents := len(l.all())
	d, created, err := reg("c", 2)
	if !errors.Is(err, ErrPendingLimit) {
		t.Fatalf("err = %v, want ErrPendingLimit", err)
	}
	wantKey(t, err, "pending_many")
	if created || d.ID != "" {
		t.Errorf("got device %+v created=%v with error", d, created)
	}
	if _, ok := s.DeviceFor(c.ID, "c"); ok {
		t.Error("a record was made for the refused worker")
	}
	if after := jsonOf(t, s.Devices(Filter{})); after != before {
		t.Error("devices changed by a refused register")
	}
	if len(l.all()) != nEvents {
		t.Error("refused register emitted an event")
	}
	// Reading the file back: no record on disk either.
	if n := len(reopen(t, s).Devices(Filter{})); n != 2 {
		t.Errorf("file has %d devices, want 2", n)
	}
	// An already known pending worker is returned, not refused, at the cap.
	if d, created, err := reg("A", 2); err != nil || created || d.Worker != "a" {
		t.Errorf("known worker at cap: %+v %v %v", d, created, err)
	}
	// Approving one frees a pending place.
	pa, _ := s.DeviceFor(c.ID, "a")
	act(t, s, pa.ID, ActApprove)
	if _, created, err := reg("c", 2); err != nil || !created {
		t.Fatalf("after approve: created=%v err=%v", created, err)
	}
	// Rejected devices are not pending.
	pb, _ := s.DeviceFor(c.ID, "b")
	act(t, s, pb.ID, ActReject)
	if _, created, err := reg("d", 2); err != nil || !created {
		t.Fatalf("after reject: created=%v err=%v", created, err)
	}
	// 0 is no limit.
	for i := 0; i < 20; i++ {
		if _, _, err := reg(fmt.Sprintf("many%d", i), 0); err != nil {
			t.Fatal(err)
		}
	}
	// The limit is per customer.
	c2, _ := mkCust(t, s, NewCustomer{Slug: "other"})
	if _, created, err := s.Register(c2, "z", "", "", 1); err != nil || !created {
		t.Fatalf("other customer: %v %v", created, err)
	}
}

func seedFile(t *testing.T, s *Store, fn func(f *file)) *Store {
	t.Helper()
	b, err := os.ReadFile(s.doc.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	fn(&f)
	b, _ = json.Marshal(f)
	if err := os.WriteFile(s.doc.Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return reopen(t, s)
}

func TestRegisterHardCaps(t *testing.T) {
	t.Run("per customer", func(t *testing.T) {
		s, _ := newStore(t)
		c, _ := mkCust(t, s, NewCustomer{})
		s = seedFile(t, s, func(f *file) {
			for i := 0; i < maxPerCustomer; i++ {
				w := fmt.Sprintf("w%d", i)
				f.Devices = append(f.Devices, Device{ID: "d" + w, CustomerID: c.ID, Worker: w, UpstreamWorker: w, Status: Rejected})
			}
		})
		_, _, err := s.Register(c, "new", "", "", 0)
		wantKey(t, err, "devices_many")
		if _, ok := s.DeviceFor(c.ID, "new"); ok {
			t.Error("record made over the cap")
		}
		// A known worker is still returned.
		if _, created, err := s.Register(c, "W5", "", "", 0); err != nil || created {
			t.Errorf("known worker: %v %v", created, err)
		}
	})
	t.Run("customers", func(t *testing.T) {
		s, _ := newStore(t)
		mkCust(t, s, NewCustomer{Slug: "first"})
		s = seedFile(t, s, func(f *file) {
			for i := 0; i < maxCustomers-1; i++ {
				f.Customers = append(f.Customers, Customer{ID: fmt.Sprintf("c%d", i), Slug: fmt.Sprintf("slug%05d", i), Name: "n", Status: CustomerActive})
			}
		})
		_, _, err := s.CreateCustomer(NewCustomer{Slug: "onemore", Name: "x"}, admin)
		wantKey(t, err, "customers_many")
	})
	t.Run("total devices", func(t *testing.T) {
		s, _ := newStore(t)
		c, _ := mkCust(t, s, NewCustomer{})
		s = seedFile(t, s, func(f *file) {
			for i := 0; i < maxDevices; i++ {
				w := fmt.Sprintf("w%d", i)
				f.Devices = append(f.Devices, Device{ID: "d" + w, CustomerID: fmt.Sprintf("other%d", i/1000), Worker: w, UpstreamWorker: w, Status: Rejected})
			}
		})
		_, _, err := s.Register(c, "new", "", "", 0)
		wantKey(t, err, "devices_many")
	})
}

// ---- 3. bypass resistance ----

func TestRevokedAndBlockedCannotReRegisterByCase(t *testing.T) {
	for _, st := range []DeviceStatus{Revoked, Blocked, Rejected, Suspended} {
		t.Run(string(st), func(t *testing.T) {
			s, _ := newStore(t)
			c, _ := mkCust(t, s, NewCustomer{})
			d := mkDevice(t, s, c, "Rig-One", st)
			for _, w := range []string{"rig-one", "RIG-ONE", "rIg-OnE", "Rig-One"} {
				got, created, err := s.Register(c, w, "ua", "ip", 5)
				if err != nil || created {
					t.Fatalf("Register(%q): created=%v err=%v", w, created, err)
				}
				if got.ID != d.ID || got.Status != st {
					t.Fatalf("Register(%q) = %+v, want the %s device", w, got, st)
				}
				if ok, why := s.Usable(got.ID); ok || why != "device_"+string(st) {
					t.Errorf("Usable = %v %q", ok, why)
				}
			}
			if n := len(s.Devices(Filter{})); n != 1 {
				t.Fatalf("%d devices, want 1", n)
			}
			// The same is true after a restart.
			if _, ok := reopen(t, s).DeviceFor(c.ID, "RIG-ONE"); !ok {
				t.Error("index lost the case-insensitive key after reopen")
			}
		})
	}
}

func TestDeleteLetsWorkerRegisterAgain(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Revoked)
	if err := s.DeleteDevice(d.ID, admin); err != nil {
		t.Fatal(err)
	}
	n, created, err := s.Register(c, "RIG", "", "", 0)
	if err != nil || !created || n.ID == d.ID || n.Status != Pending {
		t.Fatalf("re-register: %+v %v %v", n, created, err)
	}
}

func TestRegisterConcurrentSameWorkerOneDevice(t *testing.T) {
	s, _ := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	variants := []string{"Rig-A", "rig-a", "RIG-A", "rIg-a"}
	const n = 80
	var wg sync.WaitGroup
	start := make(chan struct{})
	ids := make([]string, n)
	created := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			d, cr, err := s.Register(c, variants[i%len(variants)], "ua", "ip", 10)
			ids[i], created[i], errs[i] = d.ID, cr, err
		}(i)
	}
	close(start)
	wg.Wait()
	nCreated := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		if ids[i] != ids[0] || ids[i] == "" {
			t.Fatalf("goroutine %d got id %q, goroutine 0 got %q", i, ids[i], ids[0])
		}
		if created[i] {
			nCreated++
		}
	}
	if nCreated != 1 {
		t.Errorf("created = %d times, want 1", nCreated)
	}
	if got := len(s.Devices(Filter{})); got != 1 {
		t.Errorf("%d devices, want 1", got)
	}
	if got := len(l.byType(EvDevicePending)); got != 1 {
		t.Errorf("%d pending events, want 1", got)
	}
	if got := len(reopen(t, s).Devices(Filter{})); got != 1 {
		t.Errorf("%d devices on disk, want 1", got)
	}
}

func TestRegisterConcurrentDifferentWorkersUniqueUpstream(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	// All of these make the upstream name "s211".
	var workers []string
	for _, sep := range []string{"-", ".", "_", ""} {
		for _, pre := range []string{"s", "S"} {
			workers = append(workers, pre+"21"+sep+"1")
		}
	}
	workers = append(workers, "s2-1-1", "S.2.1.1")
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w string) {
			defer wg.Done()
			if _, _, err := s.Register(c, w, "", "", 0); err != nil {
				t.Errorf("%s: %v", w, err)
			}
		}(w)
	}
	wg.Wait()
	devs := s.Devices(Filter{})
	// "s21-1" and "S21-1" are one device; "s211" and "S211" likewise.
	seen := map[string]bool{}
	for _, d := range devs {
		if seen[d.UpstreamWorker] {
			t.Errorf("duplicate upstream name %q", d.UpstreamWorker)
		}
		seen[d.UpstreamWorker] = true
		if !regexp.MustCompile(`^[a-z0-9]{1,64}$`).MatchString(d.UpstreamWorker) {
			t.Errorf("bad upstream %q", d.UpstreamWorker)
		}
	}
	if len(devs) != 6 { // s21-1, s21.1, s21_1, s211, s2-1-1, S.2.1.1 (lowercase s.2.1.1)
		t.Errorf("%d devices, want 6: %+v", len(devs), devs)
	}
}

func TestUpstreamName(t *testing.T) {
	re := regexp.MustCompile(`^[a-z0-9]{1,64}$`)
	cases := map[string]string{
		"S21-0042":    "s210042",
		"rack1.s21-1": "rack1s211",
		"abc":         "abc",
		"Ünï-1":       "n1",
		"---":         "w",
		"":            "w",
		"ÄÖ":          "w",
	}
	for in, want := range cases {
		if got := UpstreamName(in); got != want {
			t.Errorf("UpstreamName(%q) = %q, want %q", in, got, want)
		}
	}
	long := UpstreamName(strings.Repeat("A1-", 100))
	if len(long) != MaxUpstreamWorker || !re.MatchString(long) {
		t.Errorf("long name = %q", long)
	}
}

func TestUpstreamNamesUniquePerCustomerAndBounded(t *testing.T) {
	s, _ := newStore(t)
	c1, _ := mkCust(t, s, NewCustomer{Slug: "one"})
	c2, _ := mkCust(t, s, NewCustomer{Slug: "two"})
	re := regexp.MustCompile(`^[a-z0-9]{1,64}$`)

	a := register(t, s, c1, "S21-1")
	b := register(t, s, c1, "s21.1")
	c := register(t, s, c1, "s21_1")
	d := register(t, s, c1, "s211")
	if a.UpstreamWorker != "s211" || b.UpstreamWorker != "s2112" || c.UpstreamWorker != "s2113" || d.UpstreamWorker != "s2114" {
		t.Errorf("names: %q %q %q %q", a.UpstreamWorker, b.UpstreamWorker, c.UpstreamWorker, d.UpstreamWorker)
	}
	// The other customer may have the same name: names are per customer.
	if x := register(t, s, c2, "S21-1"); x.UpstreamWorker != "s211" {
		t.Errorf("customer two got %q", x.UpstreamWorker)
	}
	// The name is fixed when first seen, also after a restart.
	if got, _ := reopen(t, s).DeviceFor(c1.ID, "s21.1"); got.UpstreamWorker != "s2112" {
		t.Errorf("after reopen %q", got.UpstreamWorker)
	}

	// A 63-letter base with one punctuation mark in any place: 63 distinct
	// workers, all with the same upstream base. From the ninth collision on
	// the suffix has two digits and the base is cut to stay within 64.
	var ws []string
	seen := map[string]bool{}
	for k := 1; k <= 20; k++ {
		ws = append(ws, strings.Repeat("a", k)+"-"+strings.Repeat("a", 63-k))
	}
	for _, w := range ws {
		if !ValidWorker(w) {
			t.Fatalf("test worker %q invalid", w)
		}
		dev := register(t, s, c1, w)
		if !re.MatchString(dev.UpstreamWorker) {
			t.Errorf("upstream %q for %q not lowercase alnum <=64", dev.UpstreamWorker, w)
		}
		if seen[dev.UpstreamWorker] {
			t.Errorf("duplicate upstream %q", dev.UpstreamWorker)
		}
		seen[dev.UpstreamWorker] = true
	}
	if len(seen) != len(ws) {
		t.Errorf("%d unique of %d", len(seen), len(ws))
	}
}

func TestRegisterRejectsInvalidWorker(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	for _, w := range []string{"", ".x", "-x", "a b", "a/b", "a'b", "نام", strings.Repeat("a", 65), "a\nb"} {
		_, created, err := s.Register(c, w, "", "", 0)
		wantKey(t, err, "worker_invalid")
		if created {
			t.Errorf("%q created", w)
		}
	}
	if n := len(s.Devices(Filter{})); n != 0 {
		t.Errorf("%d devices after invalid workers", n)
	}
	for _, w := range []string{"a", "A", "0", "rack1.s21-01", "a_b", strings.Repeat("a", 64)} {
		if !ValidWorker(w) {
			t.Errorf("%q should be valid", w)
		}
	}
}

func TestRegisterFillsPendingDevice(t *testing.T) {
	s, clk := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	ua := strings.Repeat("u", 300)
	d, created, err := s.Register(c, "Rig1", ua, "9.9.9.9", 0)
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if d.Status != Pending || d.StatusBy != "system" || d.CustomerID != c.ID || d.Worker != "Rig1" ||
		d.LastIP != "9.9.9.9" || len(d.UserAgent) != 128 || !d.FirstSeen.Equal(clk.Now()) || !d.LastSeen.Equal(clk.Now()) {
		t.Errorf("device = %+v", d)
	}
	evs := l.byType(EvDevicePending)
	if len(evs) != 1 {
		t.Fatalf("events %+v", l.all())
	}
	e := evs[0]
	if e.CustomerID != c.ID || e.DeviceID != d.ID || e.Worker != "Rig1" || e.To != "pending" || e.IP != "9.9.9.9" ||
		e.Actor.Type != audit.ActorDevice || e.Actor.Name != "acme.Rig1" {
		t.Errorf("event = %+v", e)
	}
}

// ---- 4. keys ----

func TestKeyLifecycle(t *testing.T) {
	s, _ := newStore(t)
	c, key := mkCust(t, s, NewCustomer{})
	if len(key) != KeyLength {
		t.Fatalf("key length %d", len(key))
	}
	for _, r := range key {
		if !strings.ContainsRune(keyAlphabet, r) {
			t.Fatalf("key %q has %q outside the alphabet", key, r)
		}
	}
	if !CheckKey(c, key) {
		t.Fatal("key rejected")
	}
	stored, _ := s.Customer(c.ID)
	if !CheckKey(stored, key) {
		t.Fatal("stored customer rejects the key")
	}
	for _, bad := range []string{"", "x", key + "x", key[:len(key)-1], strings.ToUpper(key), " " + key, key + "\n"} {
		if CheckKey(stored, bad) {
			t.Errorf("CheckKey accepted %q", bad)
		}
	}
	if CheckKey(Customer{}, key) || CheckKey(Customer{}, "") {
		t.Error("a customer with no hash accepts a key")
	}
	if CheckKey(dummyCustomer, key) || CheckKey(dummyCustomer, "") {
		t.Error("dummy customer accepts a key")
	}

	// Rotation invalidates the old key at once.
	nk, err := s.RotateKey(c.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	if nk == key || len(nk) != KeyLength {
		t.Fatalf("new key %q", nk)
	}
	after, _ := s.Customer(c.ID)
	if CheckKey(after, key) {
		t.Error("old key still works after rotation")
	}
	if !CheckKey(after, nk) {
		t.Error("new key rejected")
	}
	if after.KeySalt == stored.KeySalt || after.KeyHash == stored.KeyHash {
		t.Error("salt or hash unchanged by rotation")
	}
	if !after.KeyRotatedAt.Equal(s.now().UTC()) {
		t.Errorf("KeyRotatedAt = %v", after.KeyRotatedAt)
	}

	// Survives a reopen.
	r := reopen(t, s)
	rc, _ := r.CustomerBySlug("acme")
	if !CheckKey(rc, nk) || CheckKey(rc, key) {
		t.Error("keys wrong after reopen")
	}

	if _, err := s.RotateKey("nope", admin); !errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("rotate unknown: %v", err)
	}
}

func TestKeyNeverStoredInPlaintext(t *testing.T) {
	s, _ := newStore(t)
	_, k1 := mkCust(t, s, NewCustomer{Slug: "one"})
	c2, k2 := mkCust(t, s, NewCustomer{Slug: "two"})
	k3, err := s.RotateKey(c2.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	s.Flush()
	b, err := os.ReadFile(s.doc.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{k1, k2, k3} {
		if strings.Contains(string(b), k) {
			t.Errorf("file contains key %q", k)
		}
	}
	if !strings.Contains(string(b), `"key_hash"`) || !strings.Contains(string(b), `"key_salt"`) {
		t.Error("file has no hash and salt")
	}
	// Nor in any event or in what the store returns beyond the one-time key.
	l := logEvents(s)
	if _, err := s.RotateKey(c2.ID, admin); err != nil {
		t.Fatal(err)
	}
	for _, e := range l.all() {
		if strings.Contains(fmt.Sprintf("%+v", e), k3) {
			t.Error("event leaks key")
		}
	}
}

func TestKeyHashIsSalted(t *testing.T) {
	const key = "samekeysamekeysamekey234"
	s1, s2 := newSalt(), newSalt()
	if s1 == s2 {
		t.Fatal("salts repeat")
	}
	h1, h2 := hashKey(s1, key), hashKey(s2, key)
	if h1 == h2 {
		t.Error("same key, different salt, same hash")
	}
	c1 := Customer{KeySalt: s1, KeyHash: h1}
	c2 := Customer{KeySalt: s2, KeyHash: h2}
	if !CheckKey(c1, key) || !CheckKey(c2, key) {
		t.Error("check failed")
	}
	// c1's hash with c2's salt does not verify.
	if CheckKey(Customer{KeySalt: s2, KeyHash: h1}, key) {
		t.Error("hash verified under another salt")
	}
	if len(h1) != 64 {
		t.Errorf("hash %q", h1)
	}
	// Customers made by the store get their own salts.
	s, _ := newStore(t)
	a, _ := mkCust(t, s, NewCustomer{Slug: "one"})
	b, _ := mkCust(t, s, NewCustomer{Slug: "two"})
	if a.KeySalt == b.KeySalt || a.KeyHash == b.KeyHash || a.KeySalt == "" {
		t.Error("customers share salt or hash")
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k := NewKey()
		if seen[k] {
			t.Fatal("NewKey repeated")
		}
		seen[k] = true
	}
}

// ---- 5. customer lifecycle ----

func TestValidators(t *testing.T) {
	slugs := map[string]bool{
		"abc": true, "a_b": true, "a-b-c": true, "0ab": true, strings.Repeat("a", 32): true,
		"ab": false, "": false, "-abc": false, "_abc": false, "ab c": false, "a.b": false, "ABC": false,
		strings.Repeat("a", 33): false, "نام": false, "a/b": false,
	}
	for s, want := range slugs {
		if ValidSlug(s) != want {
			t.Errorf("ValidSlug(%q) = %v", s, !want)
		}
	}
	accounts := map[string]bool{
		"a": true, "Acme_1-x": true, strings.Repeat("a", 64): true,
		"": false, "a.b": false, "a b": false, "-a": false, "_a": false, "a'b": false, strings.Repeat("a", 65): false,
	}
	for s, want := range accounts {
		if ValidAccount(s) != want {
			t.Errorf("ValidAccount(%q) = %v", s, !want)
		}
	}
}

func TestSplitLogin(t *testing.T) {
	cases := []struct{ in, slug, worker string }{
		{"acme.rack1.s21-01", "acme", "rack1.s21-01"},
		{"ACME.Rig", "acme", "Rig"},
		{"acme", "acme", "default"},
		{"acme.", "acme", "default"},
		{"  acme.rig  ", "acme", "rig"},
		{"", "", "default"},
		{".rig", "", "rig"},
	}
	for _, c := range cases {
		slug, w := SplitLogin(c.in)
		if slug != c.slug || w != c.worker {
			t.Errorf("SplitLogin(%q) = %q,%q want %q,%q", c.in, slug, w, c.slug, c.worker)
		}
	}
}

func TestCreateCustomerValidation(t *testing.T) {
	s, _ := newStore(t)
	l := logEvents(s)
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		name string
		in   NewCustomer
		key  string
		fld  string
	}{
		{"slug short", NewCustomer{Slug: "ab", Name: "x"}, "slug_invalid", "slug"},
		{"slug empty", NewCustomer{Slug: "", Name: "x"}, "slug_invalid", "slug"},
		{"slug dash", NewCustomer{Slug: "-abc", Name: "x"}, "slug_invalid", "slug"},
		{"slug dot", NewCustomer{Slug: "a.bc", Name: "x"}, "slug_invalid", "slug"},
		{"slug long", NewCustomer{Slug: long(33), Name: "x"}, "slug_invalid", "slug"},
		{"name empty", NewCustomer{Slug: "abc", Name: ""}, "name_required", "name"},
		{"name blank", NewCustomer{Slug: "abc", Name: "   "}, "name_required", "name"},
		{"name long", NewCustomer{Slug: "abc", Name: long(65)}, "name_required", "name"},
		{"account dot", NewCustomer{Slug: "abc", Name: "x", Account: "a.b"}, "account_invalid", "account"},
		{"account space", NewCustomer{Slug: "abc", Name: "x", Account: "a b"}, "account_invalid", "account"},
		{"account long", NewCustomer{Slug: "abc", Name: "x", Account: long(65)}, "account_invalid", "account"},
		{"language", NewCustomer{Slug: "abc", Name: "x", Language: "de"}, "language_unknown", "language"},
		{"language case", NewCustomer{Slug: "abc", Name: "x", Language: "FA"}, "language_unknown", "language"},
		{"notes", NewCustomer{Slug: "abc", Name: "x", Notes: long(501)}, "notes_long", "notes"},
		{"max devices neg", NewCustomer{Slug: "abc", Name: "x", MaxDevices: -1}, "max_devices_range", "max_devices"},
		{"max devices big", NewCustomer{Slug: "abc", Name: "x", MaxDevices: maxLimit + 1}, "max_devices_range", "max_devices"},
		{"max conns neg", NewCustomer{Slug: "abc", Name: "x", MaxConnections: -1}, "max_connections_range", "max_connections"},
		{"max conns big", NewCustomer{Slug: "abc", Name: "x", MaxConnections: maxLimit + 1}, "max_connections_range", "max_connections"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, key, err := s.CreateCustomer(c.in, admin)
			wantKey(t, err, c.key)
			var e *Error
			errors.As(err, &e)
			if e.Field != c.fld {
				t.Errorf("field = %q, want %q", e.Field, c.fld)
			}
			if key != "" || got.ID != "" {
				t.Errorf("got key %q / customer %+v with an error", key, got)
			}
		})
	}
	if n := len(s.Customers()); n != 0 {
		t.Errorf("%d customers after failures", n)
	}
	if n := len(l.all()); n != 0 {
		t.Errorf("%d events after failures", n)
	}
	if _, err := os.Stat(s.doc.Path()); err == nil {
		t.Error("file written for failed creates")
	}

	// Valid edge values.
	for _, in := range []NewCustomer{
		{Slug: "abc", Name: long(64), Account: long(64), Language: "fa", MaxDevices: maxLimit, MaxConnections: maxLimit, Notes: long(500)},
		{Slug: "xyz", Name: "ن" + strings.Repeat("ی", 63)}, // 64 runes, more bytes
	} {
		if _, _, err := s.CreateCustomer(in, admin); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
}

func TestCreateCustomerNormalizes(t *testing.T) {
	s, clk := newStore(t)
	exp := clk.Now().Add(24 * time.Hour)
	c, _, err := s.CreateCustomer(NewCustomer{Slug: "  ACME-1 ", Name: "  Acme Co ", Account: " acc_1 ", Notes: " n ", Language: "ru", ExpiresAt: &exp}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if c.Slug != "acme-1" || c.Name != "Acme Co" || c.Account != "acc_1" || c.Notes != "n" || c.Status != CustomerActive ||
		c.ExpiresAt == nil || !c.ExpiresAt.Equal(exp) || !c.CreatedAt.Equal(clk.Now()) || c.ID == "" {
		t.Errorf("customer = %+v", c)
	}
	if got, ok := s.CustomerBySlug("ACME-1"); !ok || got.ID != c.ID {
		t.Error("CustomerBySlug is case sensitive")
	}
	// Slugs are unique, case-insensitively.
	_, _, err = s.CreateCustomer(NewCustomer{Slug: "Acme-1", Name: "Other"}, admin)
	wantKey(t, err, "slug_taken")
	if _, ok := s.CustomerBySlug("nobody"); ok {
		t.Error("found unknown slug")
	}
	if _, ok := s.Customer("nobody"); ok {
		t.Error("found unknown id")
	}
}

func TestCustomersOrderedByName(t *testing.T) {
	s, _ := newStore(t)
	mkCust(t, s, NewCustomer{Slug: "zz1", Name: "beta"})
	mkCust(t, s, NewCustomer{Slug: "aa2", Name: "Alpha"})
	mkCust(t, s, NewCustomer{Slug: "aa1", Name: "alpha"})
	var got []string
	for _, c := range s.Customers() {
		got = append(got, c.Slug)
	}
	if want := []string{"aa1", "aa2", "zz1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("order %v want %v", got, want)
	}
}

func TestUpdateCustomer(t *testing.T) {
	s, clk := newStore(t)
	l := logEvents(s)
	c, key := mkCust(t, s, NewCustomer{Account: "acc", Language: "en", Notes: "note", MaxDevices: 3, MaxConnections: 2})
	clk.Advance(time.Hour)

	name, md := "New Name", 5
	got, err := s.UpdateCustomer(c.ID, CustomerUpdate{Name: &name, MaxDevices: &md}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "New Name" || got.MaxDevices != 5 || got.Account != "acc" || got.Language != "en" || got.Notes != "note" ||
		got.MaxConnections != 2 || got.Slug != "acme" || got.Status != CustomerActive {
		t.Errorf("updated = %+v", got)
	}
	if !got.UpdatedAt.Equal(clk.Now()) || !got.CreatedAt.Before(got.UpdatedAt) {
		t.Errorf("times %v %v", got.CreatedAt, got.UpdatedAt)
	}
	if !CheckKey(got, key) {
		t.Error("update broke the key")
	}
	evs := l.byType(EvCustomerUpdated)
	if len(evs) != 1 || evs[0].Details["name"] != "New Name" || evs[0].Details["max_devices"] != 5 || len(evs[0].Details) != 2 {
		t.Errorf("events %+v", evs)
	}

	// Expiry set and cleared.
	exp := clk.Now().Add(time.Hour).In(time.FixedZone("x", 3600))
	got, _ = s.UpdateCustomer(c.ID, CustomerUpdate{ExpiresAt: &exp}, admin)
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) || got.ExpiresAt.Location() != time.UTC {
		t.Errorf("expires = %v", got.ExpiresAt)
	}
	got, _ = s.UpdateCustomer(c.ID, CustomerUpdate{ClearExpires: true, ExpiresAt: &exp}, admin)
	if got.ExpiresAt != nil {
		t.Errorf("ClearExpires left %v", got.ExpiresAt)
	}

	// Invalid updates change nothing, on disk or in memory.
	before := jsonOf(t, s.Customers())
	nEv := len(l.all())
	bad := "xx"
	empty, neg, longNotes := "  ", -1, strings.Repeat("n", 501)
	badAcc := "a.b"
	for name, u := range map[string]struct {
		u   CustomerUpdate
		key string
	}{
		"language": {CustomerUpdate{Language: &bad}, "language_unknown"},
		"name":     {CustomerUpdate{Name: &empty}, "name_required"},
		"devices":  {CustomerUpdate{MaxDevices: &neg}, "max_devices_range"},
		"conns":    {CustomerUpdate{MaxConnections: &neg}, "max_connections_range"},
		"notes":    {CustomerUpdate{Notes: &longNotes}, "notes_long"},
		"account":  {CustomerUpdate{Account: &badAcc}, "account_invalid"},
	} {
		_, err := s.UpdateCustomer(c.ID, u.u, admin)
		if errKey(err) != u.key {
			t.Errorf("%s: err = %v, want %s", name, err, u.key)
		}
	}
	if after := jsonOf(t, s.Customers()); after != before {
		t.Error("invalid update changed the customer")
	}
	if len(l.all()) != nEv {
		t.Error("invalid update emitted events")
	}
	if got, _ := reopen(t, s).Customer(c.ID); jsonOf(t, got) != jsonOf(t, s.Customers()[0]) {
		t.Error("disk differs from memory")
	}
	// Clearing the account is allowed.
	got, err = s.UpdateCustomer(c.ID, CustomerUpdate{Account: new(string)}, admin)
	if err != nil || got.Account != "" {
		t.Errorf("clear account: %v %q", err, got.Account)
	}
	if _, err := s.UpdateCustomer("nope", CustomerUpdate{}, admin); !errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestSetCustomerStatus(t *testing.T) {
	s, clk := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	clk.Advance(time.Minute)
	got, err := s.SetCustomerStatus(c.ID, CustomerSuspended, "  late payment ", admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != CustomerSuspended || got.StatusReason != "late payment" || !got.StatusAt.Equal(clk.Now()) {
		t.Errorf("customer = %+v", got)
	}
	if _, err = s.SetCustomerStatus(c.ID, CustomerBlocked, "", admin); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetCustomerStatus(c.ID, CustomerActive, "", admin); err != nil {
		t.Fatal(err)
	}
	evs := l.byType(EvCustomerStatus)
	if len(evs) != 3 {
		t.Fatalf("%d status events", len(evs))
	}
	want := [][3]string{{"active", "suspended", "late payment"}, {"suspended", "blocked", ""}, {"blocked", "active", ""}}
	for i, e := range evs {
		if e.From != want[i][0] || e.To != want[i][1] || e.Reason != want[i][2] || e.CustomerID != c.ID {
			t.Errorf("event %d = %+v", i, e)
		}
	}
	_, err = s.SetCustomerStatus(c.ID, "weird", "", admin)
	wantKey(t, err, "status_invalid")
	_, err = s.SetCustomerStatus(c.ID, CustomerBlocked, strings.Repeat("r", 201), admin)
	wantKey(t, err, "reason_long")
	if now, _ := s.Customer(c.ID); now.Status != CustomerActive {
		t.Errorf("failed change moved status to %q", now.Status)
	}
	if _, err := s.SetCustomerStatus("nope", CustomerBlocked, "", admin); !errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if len(l.byType(EvCustomerStatus)) != 3 {
		t.Error("failures emitted events")
	}
	// Device state is left alone by a customer state change.
	d := mkDevice(t, s, c, "rig", Approved)
	s.SetCustomerStatus(c.ID, CustomerBlocked, "", admin)
	if got, _ := s.Device(d.ID); got.Status != Approved {
		t.Errorf("device status = %q", got.Status)
	}
	if ok, why := s.Usable(d.ID); ok || why != "customer_blocked" {
		t.Errorf("Usable = %v %q", ok, why)
	}
}

func TestDeleteCustomerRefusedWhileDevicesExist(t *testing.T) {
	s, _ := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	other, _ := mkCust(t, s, NewCustomer{Slug: "other"})
	d := mkDevice(t, s, c, "rig", Pending)

	// A device in any state stops the delete; nothing is cascaded.
	for _, st := range []DeviceStatus{Pending, Approved, Rejected, Suspended, Blocked, Revoked} {
		s2, _ := newStore(t)
		c2, _ := mkCust(t, s2, NewCustomer{})
		d2 := mkDevice(t, s2, c2, "rig", st)
		wantKey(t, s2.DeleteCustomer(c2.ID, admin), "customer_has_devices")
		if _, ok := s2.Customer(c2.ID); !ok {
			t.Fatalf("customer deleted while the device is %s", st)
		}
		if _, ok := s2.Device(d2.ID); !ok {
			t.Fatalf("device cascaded away while %s", st)
		}
	}
	act(t, s, d.ID, ActApprove)
	act(t, s, d.ID, ActRevoke)
	wantKey(t, s.DeleteCustomer(c.ID, admin), "customer_has_devices")
	if n := len(l.byType(EvCustomerDeleted)); n != 0 {
		t.Errorf("%d delete events after refusals", n)
	}
	// Once the devices are deleted the customer can go; others are untouched.
	od := mkDevice(t, s, other, "rig", Pending)
	if err := s.DeleteDevice(d.ID, admin); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCustomer(c.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Customer(c.ID); ok {
		t.Error("customer still there")
	}
	if _, ok := s.CustomerBySlug("acme"); ok {
		t.Error("slug still indexed")
	}
	if _, ok := s.Device(od.ID); !ok {
		t.Error("other customer's device lost")
	}
	evs := l.byType(EvCustomerDeleted)
	if len(evs) != 1 || evs[0].CustomerID != c.ID || evs[0].CustomerSlug != "acme" {
		t.Errorf("events %+v", evs)
	}
	if err := s.DeleteCustomer(c.ID, admin); !errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("second delete: %v", err)
	}
	// The slug is free again and the new customer is a different one.
	n, _ := mkCust(t, s, NewCustomer{})
	if n.ID == c.ID {
		t.Error("id reused")
	}
	if _, ok := reopen(t, s).CustomerBySlug("other"); !ok {
		t.Error("other customer lost on disk")
	}
}

func TestDeleteDevice(t *testing.T) {
	for _, st := range []DeviceStatus{Pending, Rejected, Blocked, Revoked} {
		t.Run("ok/"+string(st), func(t *testing.T) {
			s, _ := newStore(t)
			l := logEvents(s)
			c, _ := mkCust(t, s, NewCustomer{})
			d := mkDevice(t, s, c, "rig", st)
			s.NoteSeen(d.ID, "ua", "1.1.1.1", true)
			if err := s.DeleteDevice(d.ID, admin); err != nil {
				t.Fatal(err)
			}
			if _, ok := s.Device(d.ID); ok {
				t.Error("device still there")
			}
			if _, ok := s.DeviceFor(c.ID, "RIG"); ok {
				t.Error("key still indexed")
			}
			if ok, why := s.Usable(d.ID); ok || why != "device_deleted" {
				t.Errorf("Usable = %v %q", ok, why)
			}
			// A flush after delete must not bring the touch back.
			if err := s.Flush(); err != nil {
				t.Fatal(err)
			}
			if n := len(reopen(t, s).Devices(Filter{})); n != 0 {
				t.Errorf("%d devices on disk", n)
			}
			evs := l.byType(EvDeviceDeleted)
			if len(evs) != 1 || evs[0].From != string(st) || evs[0].DeviceID != d.ID || evs[0].Worker != "rig" {
				t.Errorf("events %+v", evs)
			}
		})
	}
	for _, st := range []DeviceStatus{Approved, Suspended} {
		t.Run("refused/"+string(st), func(t *testing.T) {
			s, _ := newStore(t)
			c, _ := mkCust(t, s, NewCustomer{})
			d := mkDevice(t, s, c, "rig", st)
			wantKey(t, s.DeleteDevice(d.ID, admin), "transition_denied")
			if _, ok := s.Device(d.ID); !ok {
				t.Error("device deleted")
			}
		})
	}
	s, _ := newStore(t)
	if err := s.DeleteDevice("nope", admin); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestUpdateDevice(t *testing.T) {
	s, clk := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Approved)
	n, exp := 3, clk.Now().Add(time.Hour)
	notes := "  hello  "
	got, err := s.UpdateDevice(d.ID, DeviceUpdate{MaxConnections: &n, ExpiresAt: &exp, Notes: &notes}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if got.MaxConnections != 3 || got.Notes != "hello" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("device = %+v", got)
	}
	if got.Status != Approved || len(got.History) != len(d.History) {
		t.Error("update changed status or history")
	}
	evs := l.byType(EvDeviceUpdated)
	if len(evs) != 1 || evs[0].DeviceID != d.ID || evs[0].Details["max_connections"] != 3 || evs[0].Details["expires_at"] == nil {
		t.Errorf("events %+v", evs)
	}
	got, _ = s.UpdateDevice(d.ID, DeviceUpdate{ClearExpires: true}, admin)
	if got.ExpiresAt != nil {
		t.Error("expiry not cleared")
	}
	if evs := l.byType(EvDeviceUpdated); evs[1].Details["expires_at"] != "none" {
		t.Errorf("event %+v", evs[1])
	}
	neg, big := -1, maxLimit+1
	_, err = s.UpdateDevice(d.ID, DeviceUpdate{MaxConnections: &neg}, admin)
	wantKey(t, err, "max_connections_range")
	_, err = s.UpdateDevice(d.ID, DeviceUpdate{MaxConnections: &big}, admin)
	wantKey(t, err, "max_connections_range")
	long := strings.Repeat("n", 501)
	_, err = s.UpdateDevice(d.ID, DeviceUpdate{Notes: &long}, admin)
	wantKey(t, err, "notes_long")
	if cur, _ := s.Device(d.ID); cur.MaxConnections != 3 || cur.Notes != "hello" {
		t.Errorf("failed update changed device: %+v", cur)
	}
	if _, err := s.UpdateDevice("nope", DeviceUpdate{}, admin); !errors.Is(err, ErrDeviceNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestUsableRule(t *testing.T) {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Second), now.Add(time.Second)
	dev := func(st DeviceStatus, exp *time.Time) Device { return Device{Status: st, ExpiresAt: exp} }
	cust := func(st CustomerStatus, exp *time.Time) Customer { return Customer{Status: st, ExpiresAt: exp} }
	cases := []struct {
		name string
		c    Customer
		d    Device
		ok   bool
		why  string
	}{
		{"ok", cust(CustomerActive, nil), dev(Approved, nil), true, ""},
		{"ok with future expiries", cust(CustomerActive, &future), dev(Approved, &future), true, ""},
		{"customer suspended", cust(CustomerSuspended, nil), dev(Approved, nil), false, "customer_suspended"},
		{"customer blocked", cust(CustomerBlocked, nil), dev(Approved, nil), false, "customer_blocked"},
		{"customer expired", cust(CustomerActive, &past), dev(Approved, nil), false, "customer_expired"},
		{"customer expiry is exclusive", cust(CustomerActive, &now), dev(Approved, nil), false, "customer_expired"},
		{"device expired", cust(CustomerActive, nil), dev(Approved, &past), false, "device_expired"},
		{"device expiry is exclusive", cust(CustomerActive, nil), dev(Approved, &now), false, "device_expired"},
		{"blocked customer beats expired", cust(CustomerBlocked, &past), dev(Approved, &past), false, "customer_blocked"},
		{"suspended customer beats blocked device", cust(CustomerSuspended, nil), dev(Blocked, nil), false, "customer_suspended"},
		{"expired customer beats pending device", cust(CustomerActive, &past), dev(Pending, nil), false, "customer_expired"},
		{"status beats device expiry", cust(CustomerActive, nil), dev(Blocked, &past), false, "device_blocked"},
		{"pending", cust(CustomerActive, nil), dev(Pending, nil), false, "device_pending"},
		{"rejected", cust(CustomerActive, nil), dev(Rejected, nil), false, "device_rejected"},
		{"suspended", cust(CustomerActive, nil), dev(Suspended, nil), false, "device_suspended"},
		{"blocked", cust(CustomerActive, nil), dev(Blocked, nil), false, "device_blocked"},
		{"revoked", cust(CustomerActive, nil), dev(Revoked, nil), false, "device_revoked"},
	}
	for _, c := range cases {
		ok, why := Usable(c.c, c.d, now)
		if ok != c.ok || why != c.why {
			t.Errorf("%s: got %v %q, want %v %q", c.name, ok, why, c.ok, c.why)
		}
	}
}

func TestStoreUsableUsesClock(t *testing.T) {
	s, clk := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Approved)
	exp := clk.Now().Add(time.Hour)
	if _, err := s.UpdateDevice(d.ID, DeviceUpdate{ExpiresAt: &exp}, admin); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Usable(d.ID); !ok {
		t.Fatal("not usable before expiry")
	}
	clk.Advance(time.Hour)
	if ok, why := s.Usable(d.ID); ok || why != "device_expired" {
		t.Errorf("at expiry: %v %q", ok, why)
	}
	// Customer expiry.
	d2 := mkDevice(t, s, c, "rig2", Approved)
	cexp := clk.Now().Add(time.Minute)
	if _, err := s.UpdateCustomer(c.ID, CustomerUpdate{ExpiresAt: &cexp}, admin); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Usable(d2.ID); !ok {
		t.Fatal("customer not expired yet")
	}
	clk.Advance(time.Minute)
	if ok, why := s.Usable(d2.ID); ok || why != "customer_expired" {
		t.Errorf("customer expired: %v %q", ok, why)
	}
	if ok, why := s.Usable("nope"); ok || why != "device_deleted" {
		t.Errorf("unknown: %v %q", ok, why)
	}
}

// ---- 7. independence ----

func TestDevicesAreIndependent(t *testing.T) {
	for _, a := range []Action{ActSuspend, ActBlock, ActRevoke} {
		t.Run(string(a), func(t *testing.T) {
			s, _ := newStore(t)
			c, _ := mkCust(t, s, NewCustomer{MaxDevices: 2})
			da := mkDevice(t, s, c, "a", Approved)
			db := mkDevice(t, s, c, "b", Approved)
			act(t, s, da.ID, a)
			if ok, _ := s.Usable(da.ID); ok {
				t.Error("A still usable")
			}
			if ok, why := s.Usable(db.ID); !ok {
				t.Errorf("B not usable: %s", why)
			}
			got, _ := s.Device(db.ID)
			if jsonOf(t, got) != jsonOf(t, db) {
				t.Error("B changed")
			}
		})
	}
}

// ---- lists, counts, touches ----

func TestDevicesFilterOrderAndCounts(t *testing.T) {
	s, _ := newStore(t)
	c1, _ := mkCust(t, s, NewCustomer{Slug: "one"})
	c2, _ := mkCust(t, s, NewCustomer{Slug: "two"})
	mkDevice(t, s, c1, "Beta", Approved)
	mkDevice(t, s, c1, "alpha", Pending)
	mkDevice(t, s, c1, "gamma", Revoked)
	mkDevice(t, s, c2, "ALPHA2", Pending)

	names := func(f Filter) string {
		var out []string
		for _, d := range s.Devices(f) {
			out = append(out, d.Worker)
		}
		return strings.Join(out, ",")
	}
	if got := names(Filter{CustomerID: c1.ID}); got != "alpha,Beta,gamma" {
		t.Errorf("order = %s", got)
	}
	if got := names(Filter{Status: Pending}); got != "alpha,ALPHA2" && got != "ALPHA2,alpha" {
		t.Errorf("pending = %s", got)
	}
	if got := names(Filter{CustomerID: c1.ID, Status: Pending}); got != "alpha" {
		t.Errorf("c1 pending = %s", got)
	}
	if got := names(Filter{Text: " ALPHA "}); len(strings.Split(got, ",")) != 2 {
		t.Errorf("text = %s", got)
	}
	if got := names(Filter{Text: "zzz"}); got != "" {
		t.Errorf("text zzz = %s", got)
	}
	cnt := s.Counts()
	if cnt.Customers[CustomerActive] != 2 || cnt.Devices[Pending] != 2 || cnt.Devices[Approved] != 1 || cnt.Devices[Revoked] != 1 {
		t.Errorf("counts = %+v", cnt)
	}
	if !Pending.Valid() || DeviceStatus("x").Valid() || !CustomerBlocked.Valid() || CustomerStatus("x").Valid() {
		t.Error("Valid")
	}
}

func TestNoteSeenAndFlush(t *testing.T) {
	s, clk := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Approved)

	clk.Advance(time.Minute)
	s.NoteSeen(d.ID, strings.Repeat("u", 200), "5.5.5.5", true)
	clk.Advance(time.Minute)
	s.NoteSeen(d.ID, "", "", true)
	s.NoteSeen(d.ID, "", "", false)
	s.NoteSeen("unknown", "x", "y", true) // no-op

	got, _ := s.Device(d.ID)
	if got.Denied != 2 || got.LastIP != "5.5.5.5" || len(got.UserAgent) != 128 || !got.LastSeen.Equal(clk.Now()) {
		t.Fatalf("in memory = %+v", got)
	}
	// Not on disk yet: a reopen sees the old values.
	if r, _ := reopen(t, s).Device(d.ID); r.Denied != 0 || r.LastIP == "5.5.5.5" {
		t.Errorf("touch reached the disk before Flush: %+v", r)
	}
	// A state change keeps the touches, once.
	act(t, s, d.ID, ActSuspend)
	got, _ = s.Device(d.ID)
	if got.Denied != 2 || got.LastIP != "5.5.5.5" || got.Status != Suspended {
		t.Fatalf("after Act = %+v", got)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.Device(d.ID)
	if got2.Denied != 2 {
		t.Errorf("Denied after flush = %d (double counted?)", got2.Denied)
	}
	if err := s.Flush(); err != nil { // nothing to do
		t.Fatal(err)
	}
	if again, _ := s.Device(d.ID); again.Denied != 2 {
		t.Errorf("second flush: %d", again.Denied)
	}
	r, _ := reopen(t, s).Device(d.ID)
	if jsonOf(t, r) != jsonOf(t, got2) {
		t.Errorf("disk:\n%s\nmemory:\n%s", jsonOf(t, r), jsonOf(t, got2))
	}
	if r.Denied != 2 || r.LastIP != "5.5.5.5" || !r.LastSeen.Equal(clk.Now()) {
		t.Errorf("reopened = %+v", r)
	}
}

func TestFlushFailureKeepsTouches(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := mkDevice(t, s, c, "rig", Approved)
	s.NoteSeen(d.ID, "ua", "7.7.7.7", true)

	path := s.doc.Path()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil { // a rename onto it fails
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err == nil {
		t.Skip("could not make the write fail on this filesystem")
	}
	if got, _ := s.Device(d.ID); got.Denied != 1 {
		t.Errorf("memory lost the touch: %+v", got)
	}
	os.RemoveAll(path)
	if err := s.Flush(); err != nil {
		t.Fatalf("retry: %v", err)
	}
	r, _ := reopen(t, s).Device(d.ID)
	if r.Denied != 1 || r.LastIP != "7.7.7.7" {
		t.Errorf("retry persisted %+v", r)
	}
}

// ---- 8. persistence ----

func TestPersistenceRoundTrip(t *testing.T) {
	s, clk := newStore(t)
	c, key := mkCust(t, s, NewCustomer{Slug: "acme", Account: "acc", Language: "fa", MaxDevices: 4, MaxConnections: 2,
		ExpiresAt: tp(clk.Now().Add(48 * time.Hour))})
	c2, _ := mkCust(t, s, NewCustomer{Slug: "beta"})
	s.SetCustomerStatus(c2.ID, CustomerSuspended, "why", admin)
	da := mkDevice(t, s, c, "A", Approved)
	db := mkDevice(t, s, c, "B", Blocked)
	dc := mkDevice(t, s, c, "C", Suspended)
	dd := mkDevice(t, s, c, "D", Revoked)
	de := mkDevice(t, s, c, "E", Rejected)
	mkDevice(t, s, c, "F", Pending)
	s.UpdateDevice(da.ID, DeviceUpdate{ExpiresAt: tp(clk.Now().Add(time.Hour)), Notes: ptr("n")}, admin)
	clk.Advance(time.Minute)
	s.NoteSeen(da.ID, "ua/1", "8.8.8.8", false)
	s.NoteSeen(db.ID, "ua/2", "8.8.4.4", true)
	s.NoteSeen(db.ID, "ua/2", "8.8.4.4", true)

	// Without Flush the touches are memory only; with it they are kept.
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	r := reopen(t, s)
	if jsonOf(t, r.Customers()) != jsonOf(t, s.Customers()) {
		t.Errorf("customers differ:\n%s\n%s", jsonOf(t, r.Customers()), jsonOf(t, s.Customers()))
	}
	if jsonOf(t, r.Devices(Filter{})) != jsonOf(t, s.Devices(Filter{})) {
		t.Errorf("devices differ:\n%s\n%s", jsonOf(t, r.Devices(Filter{})), jsonOf(t, s.Devices(Filter{})))
	}
	want := map[string]DeviceStatus{da.ID: Approved, db.ID: Blocked, dc.ID: Suspended, dd.ID: Revoked, de.ID: Rejected}
	for id, st := range want {
		if got, ok := r.Device(id); !ok || got.Status != st {
			t.Errorf("device %s = %+v, want %s", id, got, st)
		}
	}
	gb, _ := r.Device(db.ID)
	if gb.SavedStatus != Approved || gb.Denied != 2 || gb.UserAgent != "ua/2" || len(gb.History) != 2 {
		t.Errorf("blocked device = %+v", gb)
	}
	ga, _ := r.Device(da.ID)
	if !ga.LastSeen.Equal(clk.Now()) || ga.Denied != 0 || ga.LastIP != "8.8.8.8" || ga.Notes != "n" || ga.ExpiresAt == nil {
		t.Errorf("approved device = %+v", ga)
	}
	rc, ok := r.CustomerBySlug("ACME")
	if !ok || !CheckKey(rc, key) || rc.Account != "acc" || rc.Language != "fa" || rc.MaxDevices != 4 {
		t.Errorf("customer = %+v", rc)
	}
	if rc2, _ := r.CustomerBySlug("beta"); rc2.Status != CustomerSuspended || rc2.StatusReason != "why" {
		t.Errorf("customer 2 = %+v", rc2)
	}
	if d, ok := r.DeviceFor(c.ID, "a"); !ok || d.ID != da.ID {
		t.Error("DeviceFor after reopen")
	}
	// The reopened store works: limits and actions use the loaded state.
	// MaxDevices 4: approved A + suspended C hold 2; F can be approved, B can be unblocked.
	fdev, _ := r.DeviceFor(c.ID, "F")
	if _, err := r.Act(fdev.ID, ActApprove, "", admin); err != nil {
		t.Errorf("approve after reopen: %v", err)
	}
	if _, err := r.Act(db.ID, ActUnblock, "", admin); err != nil {
		t.Errorf("unblock after reopen: %v", err)
	}
	// 4 slots are now used; the rejected E cannot be approved (must reopen first), so
	// reopen it and check the limit.
	r.Act(de.ID, ActReopen, "", admin)
	_, err := r.Act(de.ID, ActApprove, "", admin)
	wantKey(t, err, "device_limit")
	// A second reopen sees the changes of the first.
	r2 := reopen(t, r)
	if got, _ := r2.Device(db.ID); got.Status != Approved {
		t.Errorf("second reopen: %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestOpenMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "devices.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Customers()) != 0 || len(s.Devices(Filter{})) != 0 {
		t.Error("not empty")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("file made on open")
	}
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("directory not made: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Errorf("flush on empty: %v", err)
	}
	mkCust(t, s, NewCustomer{})
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("file: %v %v", fi, err)
	}
}

func TestOpenCorruptFileIsAnErrorAndLeftAlone(t *testing.T) {
	for name, content := range map[string]string{
		"garbage":   "not json at all",
		"truncated": `{"customers":[{"id":"x"`,
		"empty":     "",
		"wrong":     `{"customers":"nope"}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devices.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := Open(path)
			if err == nil || s != nil {
				t.Fatalf("Open = %v, %v; want an error and no store", s, err)
			}
			if !strings.Contains(err.Error(), "parse") {
				t.Errorf("error %q", err)
			}
			b, _ := os.ReadFile(path)
			if string(b) != content {
				t.Errorf("corrupt file was modified: %q", b)
			}
		})
	}
	// JSON null or an empty object is a valid empty store.
	for _, content := range []string{`{}`, `null`, `{"customers":null,"devices":null}`} {
		path := filepath.Join(t.TempDir(), "d.json")
		os.WriteFile(path, []byte(content), 0o600)
		s, err := Open(path)
		if err != nil {
			t.Errorf("%s: %v", content, err)
			continue
		}
		if _, _, err := s.CreateCustomer(NewCustomer{Slug: "abc", Name: "x"}, admin); err != nil {
			t.Errorf("%s: create: %v", content, err)
		}
	}
	// A directory in place of the file is an error too.
	dir := t.TempDir()
	if _, err := Open(dir); err == nil {
		t.Error("opened a directory")
	}
}

// ---- 9. events ----

func TestEventsInOrderWithFields(t *testing.T) {
	s, _ := newStore(t)
	l := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{Name: "Acme Co"})
	d, _, err := s.Register(c, "Rig1", "ua", "3.3.3.3", 0)
	if err != nil {
		t.Fatal(err)
	}
	step := func(a Action, reason string) {
		if _, err := s.Act(d.ID, a, reason, admin); err != nil {
			t.Fatalf("%s: %v", a, err)
		}
	}
	step(ActReject, "r1")
	step(ActReopen, "r2")
	step(ActApprove, "r3")
	step(ActSuspend, "r4")
	step(ActResume, "r5")
	step(ActBlock, "r6")
	step(ActUnblock, "r7")
	step(ActRevoke, "r8")
	if _, err := s.UpdateDevice(d.ID, DeviceUpdate{Notes: ptr("x")}, admin); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(d.ID, admin); err != nil {
		t.Fatal(err)
	}
	nm := "Renamed"
	s.UpdateCustomer(c.ID, CustomerUpdate{Name: &nm}, admin)
	s.SetCustomerStatus(c.ID, CustomerBlocked, "bad", admin)
	s.RotateKey(c.ID, admin)
	if err := s.DeleteCustomer(c.ID, admin); err != nil {
		t.Fatal(err)
	}

	want := []struct {
		typ, from, to, reason string
		device                bool
	}{
		{EvCustomerCreated, "", "", "", false},
		{EvDevicePending, "", "pending", "", true},
		{EvDeviceRejected, "pending", "rejected", "r1", true},
		{EvDeviceReopened, "rejected", "pending", "r2", true},
		{EvDeviceApproved, "pending", "approved", "r3", true},
		{EvDeviceSuspended, "approved", "suspended", "r4", true},
		{EvDeviceResumed, "suspended", "approved", "r5", true},
		{EvDeviceBlocked, "approved", "blocked", "r6", true},
		{EvDeviceUnblocked, "blocked", "approved", "r7", true},
		{EvDeviceRevoked, "approved", "revoked", "r8", true},
		{EvDeviceUpdated, "", "", "", true},
		{EvDeviceDeleted, "revoked", "", "", true},
		{EvCustomerUpdated, "", "", "", false},
		{EvCustomerStatus, "active", "blocked", "bad", false},
		{EvCustomerKey, "", "", "", false},
		{EvCustomerDeleted, "", "", "", false},
	}
	got := l.all()
	if len(got) != len(want) {
		for _, e := range got {
			t.Logf("event %s", e.Type)
		}
		t.Fatalf("%d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		e := got[i]
		if e.Type != w.typ || e.From != w.from || e.To != w.to || e.Reason != w.reason {
			t.Errorf("event %d = %s %q->%q %q, want %s %q->%q %q", i, e.Type, e.From, e.To, e.Reason, w.typ, w.from, w.to, w.reason)
		}
		if e.CustomerID != c.ID || e.CustomerSlug != "acme" {
			t.Errorf("event %d customer = %q %q", i, e.CustomerID, e.CustomerSlug)
		}
		if e.At.IsZero() {
			t.Errorf("event %d has no time", i)
		}
		if w.device {
			if e.DeviceID != d.ID || e.Worker != "Rig1" {
				t.Errorf("event %d device = %q %q", i, e.DeviceID, e.Worker)
			}
			if tg := e.Target(); tg.Type != "device" || tg.ID != d.ID || tg.Name != "acme.Rig1" {
				t.Errorf("event %d target = %+v", i, tg)
			}
		} else {
			if e.DeviceID != "" {
				t.Errorf("event %d has device %q", i, e.DeviceID)
			}
			if tg := e.Target(); tg.Type != "customer" || tg.ID != c.ID {
				t.Errorf("event %d target = %+v", i, tg)
			}
		}
		if i > 1 && e.Type != EvDevicePending && e.Actor.Name != "admin" && e.Type != EvDeviceSuspicious {
			t.Errorf("event %d actor = %+v", i, e.Actor)
		}
	}
	// The name in events is the customer's name at the time of the event.
	if got[0].CustomerName != "Acme Co" || got[12].CustomerName != "Renamed" {
		t.Errorf("names %q %q", got[0].CustomerName, got[12].CustomerName)
	}
}

func TestEventsEveryActionHasItsOwnType(t *testing.T) {
	want := map[Action]string{
		ActApprove: "device.approve", ActReject: "device.reject", ActReopen: "device.reopen", ActSuspend: "device.suspend",
		ActResume: "device.resume", ActBlock: "device.block", ActUnblock: "device.unblock", ActRevoke: "device.revoke",
	}
	for a, typ := range want {
		if actionEvent[a] != typ {
			t.Errorf("event of %s = %q, want %q", a, actionEvent[a], typ)
		}
	}
}

func TestEventHandlersRunOutsideLocks(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig")
	var inHandler int
	s.OnEvent(func(e Event) {
		inHandler++
		// Every read method, while the event is being delivered.
		s.Customers()
		s.Customer(e.CustomerID)
		s.CustomerBySlug("acme")
		s.Devices(Filter{})
		s.Device(e.DeviceID)
		s.DeviceFor(e.CustomerID, "rig")
		s.Counts()
		s.Usable(e.DeviceID)
		s.NoteSeen(e.DeviceID, "ua", "ip", false)
		s.Flush()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		act(t, s, d.ID, ActApprove)
		act(t, s, d.ID, ActSuspend)
		s.RotateKey(c.ID, admin)
		// A handler that writes (here: from the next event) does not deadlock either.
		s.OnEvent(func(e Event) {
			if e.Type == EvDeviceResumed {
				s.UpdateDevice(d.ID, DeviceUpdate{Notes: ptr("from handler")}, admin)
			}
		})
		act(t, s, d.ID, ActResume)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock: a handler could not call back into the store")
	}
	if inHandler < 4 {
		t.Errorf("handler ran %d times", inHandler)
	}
	if got, _ := s.Device(d.ID); got.Notes != "from handler" || got.Status != Approved {
		t.Errorf("device = %+v", got)
	}
}

func TestEventHandlerOrderAndLimit(t *testing.T) {
	s, _ := newStore(t)
	var mu sync.Mutex
	var order []string
	for i := 0; i < eventHandlersMax+4; i++ {
		i := i
		s.OnEvent(func(e Event) {
			mu.Lock()
			order = append(order, fmt.Sprintf("%d:%s", i, e.Type))
			mu.Unlock()
		})
	}
	c, _ := mkCust(t, s, NewCustomer{})
	_ = c
	if len(order) != eventHandlersMax {
		t.Fatalf("%d handlers ran, want %d", len(order), eventHandlersMax)
	}
	for i, o := range order {
		if o != fmt.Sprintf("%d:%s", i, EvCustomerCreated) {
			t.Errorf("order[%d] = %s", i, o)
		}
	}
}

func TestFailedOperationsEmitNothing(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rig")
	l := logEvents(s)
	s.Act(d.ID, ActSuspend, "", admin)          // denied
	s.Act("nope", ActApprove, "", admin)        // not found
	s.Register(c, "bad name", "", "", 0)        // invalid
	s.DeleteDevice("nope", admin)               // not found
	s.DeleteCustomer(c.ID, admin)               // has devices
	s.RotateKey("nope", admin)                  // not found
	s.SetCustomerStatus(c.ID, "bad", "", admin) // invalid
	s.Register(c, "rig", "", "", 0)             // known: no new pending event
	if evs := l.all(); len(evs) != 0 {
		t.Errorf("events: %+v", evs)
	}
}
