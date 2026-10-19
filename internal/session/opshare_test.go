package session

import (
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

type fakeGate struct{}

func (fakeGate) Active() bool             { return true }
func (fakeGate) AllowConnect(string) bool { return true }
func (fakeGate) Usable(string) (bool, string) {
	return true, ""
}
func (fakeGate) Authorize(AuthRequest) AuthDecision {
	return AuthDecision{Allow: true, CustomerID: "cust", DeviceID: "dev", Account: "acme", Worker: "rig1"}
}

// fakeSplitter is a Splitter whose answers a test sets.
type fakeSplitter struct {
	active, eligible, inWindow bool
	refused                    []string
	notes                      []opshare.Outcome
	routes                     int
}

func (f *fakeSplitter) Active() bool                     { return f.active }
func (f *fakeSplitter) Eligible(_, _, _ string) bool     { return f.eligible }
func (f *fakeSplitter) OperatorRefused(_, reason string) { f.refused = append(f.refused, reason) }
func (f *fakeSplitter) Note(o opshare.Outcome)           { f.notes = append(f.notes, o) }
func (f *fakeSplitter) Route(_, _, _ string, _ time.Time) opshare.Route {
	f.routes++
	if f.eligible && f.inWindow {
		return opshare.Route{Operator: true, Window: 900}
	}
	return opshare.Route{}
}

func opSession(t *testing.T, sp *fakeSplitter, poolMarked bool) *Session {
	t.Helper()
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	up := &Upstream{PoolID: "p", PoolName: "Pool", Template: "{account}.{upstream_worker}", Password: "x"}
	if poolMarked {
		up.OperatorShare, up.OperatorUser, up.OperatorPassword = true, "op.main", "oppw"
	}
	return &Session{
		maxLine: 64 * 1024, up: up,
		srv:  &Server{Settings: set, Events: events.New(10), Stats: stats.NewCollector(), Registry: NewRegistry(events.New(10)), Gate: fakeGate{}, Splitter: sp},
		rate: stats.NewRate(time.Now()), logins: map[string]string{}, pendingAuth: map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{}, authorized: map[string]authz{}, devices: map[string]struct{}{},
	}
}

func authorizeLine(t *testing.T, s *Session) string {
	t.Helper()
	line := []byte(`{"id":1,"method":"mining.authorize","params":["acme.rig1","connection-key"]}`)
	msg, err := stratum.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.authorize(line, msg)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func submitLine(t *testing.T, s *Session, id int) string {
	t.Helper()
	line := []byte(`{"id":` + itoa(id) + `,"method":"mining.submit","params":["acme.rig1","j","0","0","0"]}`)
	msg, err := stratum.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.submit(line, msg)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestOperatorWorkerIsAskedForOnlyWithEveryCondition(t *testing.T) {
	cases := []struct {
		name   string
		sp     *fakeSplitter
		marked bool
		asks   bool
	}{
		{"all conditions", &fakeSplitter{active: true, eligible: true}, true, true},
		{"switch off", &fakeSplitter{active: false, eligible: true}, true, false},
		{"no consent or schedule (not eligible)", &fakeSplitter{active: true, eligible: false}, true, false},
		{"pool not marked", &fakeSplitter{active: true, eligible: true}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := opSession(t, c.sp, c.marked)
			out := authorizeLine(t, s)
			if got := strings.Contains(out, "bitlite-op"); got != c.asks {
				t.Fatalf("operator authorize sent = %v, want %v\n%s", got, c.asks, out)
			}
			if c.asks && !strings.Contains(out, `"op.main"`) {
				t.Fatalf("operator login missing: %s", out)
			}
			// The customer's own authorize is always first and unchanged.
			if first, _, _ := strings.Cut(out, "\n"); !strings.Contains(first, `"acme.rig1"`) || strings.Contains(first, "op.main") {
				t.Fatalf("customer's authorize altered: %s", first)
			}
		})
	}
}

func TestOperatorWorkerIsAskedForOncePerConnection(t *testing.T) {
	s := opSession(t, &fakeSplitter{active: true, eligible: true}, true)
	if !strings.Contains(authorizeLine(t, s), "bitlite-op") {
		t.Fatal("first authorize must ask")
	}
	if strings.Contains(authorizeLine(t, s), "bitlite-op") {
		t.Fatal("second authorize on the same connection must not ask again")
	}
}

func TestOperatorAnswerNeverReachesTheMinerAndEnablesRouting(t *testing.T) {
	sp := &fakeSplitter{active: true, eligible: true, inWindow: true}
	s := opSession(t, sp, true)
	authorizeLine(t, s)

	// Until the pool accepts the operator's worker nothing is routed to it.
	if got := submitLine(t, s, 10); !strings.Contains(got, `"acme.rig1"`) || strings.Contains(got, "op.main") {
		t.Fatalf("share routed before the pool accepted the operator: %s", got)
	}

	out, err := s.fromPool([]byte(`{"id":"bitlite-op","result":true,"error":null}`))
	if err != nil || out != nil {
		t.Fatalf("the operator's answer must be swallowed: %q, %v", out, err)
	}
	if got := submitLine(t, s, 11); !strings.Contains(got, `"op.main"`) || strings.Contains(got, "acme.rig1") {
		t.Fatalf("share in the window must carry the operator's worker: %s", got)
	}

	sp.inWindow = false
	if got := submitLine(t, s, 12); !strings.Contains(got, `"acme.rig1"`) || strings.Contains(got, "op.main") {
		t.Fatalf("share outside the window must carry the customer's worker: %s", got)
	}
}

func TestOperatorRefusalCostsTheCustomerNothing(t *testing.T) {
	sp := &fakeSplitter{active: true, eligible: true, inWindow: true}
	s := opSession(t, sp, true)
	authorizeLine(t, s)

	out, err := s.fromPool([]byte(`{"id":"bitlite-op","result":false,"error":[24,"unauthorized worker",null]}`))
	if err != nil || out != nil {
		t.Fatalf("a refusal must neither reach the miner nor end the session: %q, %v", out, err)
	}
	if len(sp.refused) != 1 || !strings.Contains(sp.refused[0], "unauthorized worker") {
		t.Fatalf("the splitter was not told: %v", sp.refused)
	}
	if got := submitLine(t, s, 20); !strings.Contains(got, `"acme.rig1"`) || strings.Contains(got, "op.main") {
		t.Fatalf("after a refusal every share stays the customer's: %s", got)
	}
}

func TestOutcomesAreNotedOnlyForTrackedSessions(t *testing.T) {
	sp := &fakeSplitter{active: true, eligible: true, inWindow: true}
	s := opSession(t, sp, true)
	authorizeLine(t, s)
	submitLine(t, s, 30) // before the pool accepted the operator: not tracked
	s.fromPool([]byte(`{"id":30,"result":true,"error":null}`))
	if len(sp.notes) != 0 {
		t.Fatalf("an untracked share was noted: %+v", sp.notes)
	}

	s.fromPool([]byte(`{"id":"bitlite-op","result":true,"error":null}`))
	submitLine(t, s, 31)
	s.fromPool([]byte(`{"id":31,"result":true,"error":null}`))
	sp.inWindow = false
	submitLine(t, s, 32)
	s.fromPool([]byte(`{"id":32,"result":false,"error":[23,"stale",null]}`))

	if len(sp.notes) != 2 {
		t.Fatalf("notes = %+v", sp.notes)
	}
	op, cu := sp.notes[0], sp.notes[1]
	if !op.Operator || !op.Accepted || op.Window != 900 || op.DeviceID != "dev" || op.CustomerID != "cust" || op.PoolID != "p" {
		t.Fatalf("operator outcome: %+v", op)
	}
	if cu.Operator || cu.Accepted || cu.Reason != "stale" {
		t.Fatalf("customer outcome: %+v", cu)
	}
}

func TestNoSplitterBehavesAsBefore(t *testing.T) {
	s := opSession(t, nil, true)
	s.srv.Splitter = nil
	if out := authorizeLine(t, s); strings.Contains(out, "bitlite-op") {
		t.Fatalf("a proxy without a splitter sent the operator's worker: %s", out)
	}
	if got := submitLine(t, s, 40); strings.Contains(got, "op.main") {
		t.Fatalf("a proxy without a splitter routed a share: %s", got)
	}
}
