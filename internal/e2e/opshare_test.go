package e2e

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/devices"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// users returns the first parameter (the worker) of every message with method
// the pool has received, in order.
func (p *fakePool) users(method string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.received {
		var name string
		json.Unmarshal(m["method"], &name)
		if name != method {
			continue
		}
		var params []json.RawMessage
		json.Unmarshal(m["params"], &params)
		var u string
		if len(params) > 0 {
			json.Unmarshal(params[0], &u)
		}
		out = append(out, u)
	}
	return out
}

// startSharing is a gated proxy whose pool credits a second worker and whose
// operator share takes 90 % of the mining time of every device: long enough
// windows for a test to land a share in one within seconds.
func startSharing(t *testing.T, fp *fakePool) (*gatedProxy, *opshare.Router) {
	t.Helper()
	dir := t.TempDir()
	ds, err := devices.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	aud, err := audit.Open(filepath.Join(dir, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	router := opshare.NewRouter(filepath.Join(dir, "opshare"),
		func() opshare.Config { return opshare.Config{Enabled: true, Cycle: time.Minute, MaxFraction: 1} },
		opshare.FlatRules{Fraction: func() float64 { return 0.9 }}, ds)
	g := &gatedProxy{ds: ds, aud: aud}
	g.proxy = startProxyWith(t, proxyOpts{
		template:     "{account}.{upstream_worker}",
		operatorUser: "opbtc.main",
		splitter:     router,
		gate: func(reg *session.Registry, ev *events.Log, _ *settings.Store) session.Gate {
			g.enf = &devices.Enforcement{Store: ds, Registry: reg, Audit: aud, Events: ev}
			g.enf.Install()
			return devices.NewGate(devices.GateConfig{
				Store: ds, Enabled: func() bool { return true }, PendingLimit: func() int { return 10 },
				Live: reg.LiveCustomer,
			})
		},
	}, fp)
	return g, router
}

// waitInWindow waits until the operator's window of the device is open, with
// at least a second left, using the same pure schedule the proxy uses.
func waitInWindow(t *testing.T, deviceID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		w := opshare.WindowAt(deviceID, 0.9, time.Minute, time.Now())
		if w.In && time.Until(time.Unix(w.Start, 0).Add(time.Minute)) > 3*time.Second {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the operator's window did not open")
}

func TestOperatorShareNeedsTheCustomersConsent(t *testing.T) {
	fp := newFakePool(t)
	g, _ := startSharing(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	g.connect(t, "acme.rig1", key) // registers the device as pending
	d := g.device(t, c, "rig1")
	if _, err := g.ds.Act(d.ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}

	// Without consent the operator's worker is never even asked for, and a
	// share inside the window is the customer's.
	a, r := g.connect(t, "acme.rig1", key)
	if r["result"] != "true" {
		t.Fatalf("approved device refused: %v", r)
	}
	for _, u := range fp.users("mining.authorize") {
		if u == "opbtc.main" {
			t.Fatal("the operator's worker was sent to the pool without the customer's consent")
		}
	}
	waitInWindow(t, d.ID)
	if r := a.submit(10, "acme.rig1"); r["result"] != "true" {
		t.Fatalf("share: %v", r)
	}
	if got := fp.users("mining.submit"); len(got) == 0 || got[len(got)-1] != "acmebtc.rig1" {
		t.Fatalf("without consent the share must carry the customer's worker, got %v", got)
	}
}

func TestOperatorShareIsRoutedWithConsentAndStopsWhenItIsWithdrawn(t *testing.T) {
	fp := newFakePool(t)
	g, router := startSharing(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	g.connect(t, "acme.rig1", key)
	d := g.device(t, c, "rig1")
	if _, err := g.ds.Act(d.ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ds.SetFeeConsent(c.ID, true, "terms v1", "signed agreement 12", operator); err != nil {
		t.Fatal(err)
	}

	a, r := g.connect(t, "acme.rig1", key)
	if r["result"] != "true" {
		t.Fatalf("approved device refused: %v", r)
	}
	waitFor(t, "the operator's worker reaches the pool", func() bool {
		for _, u := range fp.users("mining.authorize") {
			if u == "opbtc.main" {
				return true
			}
		}
		return false
	})

	// Inside the window the share is credited to the operator, and the ASIC
	// still gets the pool's plain answer.
	waitInWindow(t, d.ID)
	if r := a.submit(20, "acme.rig1"); r["result"] != "true" {
		t.Fatalf("operator-routed share: %v", r)
	}
	if got := fp.users("mining.submit"); got[len(got)-1] != "opbtc.main" {
		t.Fatalf("a share inside the window must carry the operator's worker, got %v", got)
	}
	waitFor(t, "the share is accounted for the operator", func() bool {
		for _, u := range router.Usage(time.Now().Add(-time.Hour), time.Now(), c.ID) {
			if u.OperatorAccepted == 1 {
				return true
			}
		}
		return false
	})

	// The consent is withdrawn: the very next share, in the same window and
	// on the same connection, is the customer's again.
	if _, err := g.ds.SetFeeConsent(c.ID, false, "", "", operator); err != nil {
		t.Fatal(err)
	}
	if r := a.submit(21, "acme.rig1"); r["result"] != "true" {
		t.Fatalf("share after withdrawal: %v", r)
	}
	if got := fp.users("mining.submit"); got[len(got)-1] != "acmebtc.rig1" {
		t.Fatalf("a share after the consent was withdrawn must be the customer's, got %v", got)
	}
}

func TestPoolRefusingTheOperatorsWorkerDoesNotHurtTheCustomer(t *testing.T) {
	fp := newFakePool(t)
	fp.mu.Lock()
	fp.rejectUser = "opbtc.main"
	fp.mu.Unlock()
	g, router := startSharing(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	g.connect(t, "acme.rig1", key)
	d := g.device(t, c, "rig1")
	g.ds.Act(d.ID, devices.ActApprove, "", operator)
	g.ds.SetFeeConsent(c.ID, true, "terms v1", "", operator)

	a, r := g.connect(t, "acme.rig1", key)
	if r["result"] != "true" {
		t.Fatalf("the customer was refused because the operator's worker was: %v", r)
	}
	waitFor(t, "the share switches itself off on the pool", func() bool { return len(router.Pools()) == 1 })

	// The connection is intact and every share is the customer's.
	waitInWindow(t, d.ID)
	if r := a.submit(30, "acme.rig1"); r["result"] != "true" {
		t.Fatalf("share after the refusal: %v", r)
	}
	if got := fp.users("mining.submit"); got[len(got)-1] != "acmebtc.rig1" {
		t.Fatalf("after a refusal every share must be the customer's, got %v", got)
	}
	if isClosed(a, 300*time.Millisecond) {
		t.Fatal("the customer's connection was closed over the operator's worker")
	}
}
