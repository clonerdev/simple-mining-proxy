package e2e

import (
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/devices"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// gatedProxy is a proxy with device control on, wired the way main.go wires
// it: the same Enforcement closes the live connections.
type gatedProxy struct {
	*proxy
	ds  *devices.Store
	enf *devices.Enforcement
	aud *audit.Log
}

var operator = audit.Actor{Type: audit.ActorUser, Name: "olga"}

func startGated(t *testing.T, pools ...*fakePool) *gatedProxy {
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
	g := &gatedProxy{ds: ds, aud: aud}
	g.proxy = startProxyWith(t, proxyOpts{
		template: "{account}.{upstream_worker}",
		gate: func(reg *session.Registry, ev *events.Log, _ *settings.Store) session.Gate {
			g.enf = &devices.Enforcement{Store: ds, Registry: reg, Audit: aud, Events: ev}
			g.enf.Install()
			return devices.NewGate(devices.GateConfig{
				Store: ds, Enabled: func() bool { return true }, PendingLimit: func() int { return 10 },
				Live: reg.LiveCustomer,
			})
		},
	}, pools...)
	return g
}

func (g *gatedProxy) customer(t *testing.T, slug, account string) (devices.Customer, string) {
	t.Helper()
	c, key, err := g.ds.CreateCustomer(devices.NewCustomer{Slug: slug, Name: strings.ToUpper(slug), Account: account}, operator)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

// connect opens an ASIC connection and sends subscribe and authorize. It
// returns the ASIC and the authorize answer.
func (g *gatedProxy) connect(t *testing.T, login, key string) (*asic, map[string]string) {
	t.Helper()
	a := dialASIC(t, g.addr)
	a.send(`{"id":2,"method":"mining.subscribe","params":["bmminer/2.0.0"]}`)
	a.response(2)
	a.send(`{"id":3,"method":"mining.authorize","params":["` + login + `","` + key + `"]}`)
	r := a.response(3)
	out := map[string]string{}
	for k, v := range r {
		out[k] = string(v)
	}
	return a, out
}

// device returns the device of a customer by worker name.
func (g *gatedProxy) device(t *testing.T, c devices.Customer, worker string) devices.Device {
	t.Helper()
	d, ok := g.ds.DeviceFor(c.ID, worker)
	if !ok {
		t.Fatalf("no device %s of %s", worker, c.Slug)
	}
	return d
}

// isClosed reports whether the proxy closed the ASIC's connection within d.
func isClosed(a *asic, d time.Duration) bool {
	a.conn.SetReadDeadline(time.Now().Add(d))
	for {
		_, err := a.r.ReadBytes('\n')
		if err == nil {
			continue // a notification: keep reading
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false
		}
		return errors.Is(err, io.EOF) || err != nil
	}
}

func (a *asic) submit(id int, login string) map[string]string {
	a.t.Helper()
	a.send(`{"id":` + itoa(id) + `,"method":"mining.submit","params":["` + login + `","job1","00000001","5f5e1000","00000002"]}`)
	r := a.response(id)
	out := map[string]string{}
	for k, v := range r {
		out[k] = string(v)
	}
	return out
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

func TestPendingDeviceCannotMineUntilApproved(t *testing.T) {
	fp := newFakePool(t)
	g := startGated(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")

	// A new worker is recorded as pending and refused. Nothing of it reaches
	// the pool: no authorize, so no hashrate and no worker appears there.
	a, r := g.connect(t, "acme.rack1", key)
	if r["result"] != "false" {
		t.Fatalf("a pending device was authorized: %v", r)
	}
	if !isClosed(a, 2*time.Second) {
		t.Fatal("the connection of a pending device stayed open")
	}
	if _, seen := fp.find("mining.authorize"); seen {
		t.Fatal("the pool saw an authorize from a pending device")
	}
	if d := g.device(t, c, "rack1"); d.Status != devices.Pending {
		t.Fatalf("status %s, want pending", d.Status)
	}

	// Approved, it mines under the customer's own account and worker.
	d := g.device(t, c, "rack1")
	if _, err := g.ds.Act(d.ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}
	a, r = g.connect(t, "acme.rack1", key)
	if r["result"] != "true" {
		t.Fatalf("an approved device was refused: %v", r)
	}
	params, _ := fp.find("mining.authorize")
	if !strings.HasPrefix(string(params), `["acmebtc.rack1",`) {
		t.Fatalf("the pool got %s, want the customer's account and worker", params)
	}
	// The ASIC's password is the connection key: it must never reach the pool.
	if strings.Contains(string(params), key) {
		t.Fatalf("the connection key was forwarded to the pool: %s", params)
	}
	if r := a.submit(10, "acme.rack1"); r["result"] != "true" {
		t.Fatalf("share of an approved device: %v", r)
	}
	waitFor(t, "the share is counted", func() bool { return g.stats.Snapshot(time.Now()).Accepted == 1 })
}

func TestBlockingOneDeviceKeepsTheCustomersOthersMining(t *testing.T) {
	fp := newFakePool(t)
	g := startGated(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	other, otherKey := g.customer(t, "globex", "globexbtc")
	for _, w := range []string{"rack1", "rack2"} {
		g.connect(t, "acme."+w, key)
		d := g.device(t, c, w)
		if _, err := g.ds.Act(d.ID, devices.ActApprove, "", operator); err != nil {
			t.Fatal(err)
		}
	}
	g.connect(t, "globex.rack1", otherKey)
	od := g.device(t, other, "rack1")
	if _, err := g.ds.Act(od.ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}

	a1, r := g.connect(t, "acme.rack1", key)
	if r["result"] != "true" {
		t.Fatalf("rack1: %v", r)
	}
	a2, r := g.connect(t, "acme.rack2", key)
	if r["result"] != "true" {
		t.Fatalf("rack2: %v", r)
	}
	a3, r := g.connect(t, "globex.rack1", otherKey)
	if r["result"] != "true" {
		t.Fatalf("globex rack1: %v", r)
	}
	waitFor(t, "three live sessions", func() bool { return g.reg.Counts().Total == 3 })

	// Blocking rack1 cuts it at once, with no share or login needed to notice.
	d1 := g.device(t, c, "rack1")
	if _, err := g.ds.Act(d1.ID, devices.ActBlock, "stolen", operator); err != nil {
		t.Fatal(err)
	}
	if !isClosed(a1, 2*time.Second) {
		t.Fatal("the blocked device stayed connected")
	}

	// The customer's other device and the other customer keep mining.
	if r := a2.submit(20, "acme.rack2"); r["result"] != "true" {
		t.Fatalf("the customer's other device was affected: %v", r)
	}
	if r := a3.submit(21, "globex.rack1"); r["result"] != "true" {
		t.Fatalf("another customer's device was affected: %v", r)
	}

	// It cannot come back by reconnecting, by repeating the request, or by
	// changing the case of its name.
	for i, login := range []string{"acme.rack1", "acme.rack1", "ACME.RACK1", "Acme.Rack1"} {
		a, r := g.connect(t, login, key)
		if r["result"] != "false" {
			t.Fatalf("attempt %d (%s): a blocked device was authorized: %v", i, login, r)
		}
		if !isClosed(a, 2*time.Second) {
			t.Fatalf("attempt %d: connection stayed open", i)
		}
	}
	if n := len(g.ds.Devices(devices.Filter{CustomerID: c.ID})); n != 2 {
		t.Fatalf("the customer has %d devices: a renamed login made a new one", n)
	}

	// Unblocking brings back the state it was in.
	if _, err := g.ds.Act(d1.ID, devices.ActUnblock, "", operator); err != nil {
		t.Fatal(err)
	}
	if _, r := g.connect(t, "acme.rack1", key); r["result"] != "true" {
		t.Fatalf("an unblocked device was refused: %v", r)
	}

	// Every step is in the audit log.
	for _, action := range []string{devices.EvDeviceBlocked, devices.EvDeviceUnblocked, devices.EvDeviceApproved} {
		got, _ := g.aud.Query(audit.Query{Action: action, Limit: 10})
		if len(got) == 0 {
			t.Errorf("no audit entry for %s", action)
		}
	}
	got, _ := g.aud.Query(audit.Query{Action: devices.EvDeviceBlocked, Limit: 1})
	// The log is JSON on disk, so the number comes back as a float.
	if len(got) == 1 && fmt.Sprint(got[0].Details["connections_closed"]) != "1" {
		t.Errorf("the audit entry should record the closed connection: %v", got[0].Details)
	}
}

func TestSuspendingACustomerCutsOnlyThatCustomer(t *testing.T) {
	fp := newFakePool(t)
	g := startGated(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	o, okey := g.customer(t, "globex", "globexbtc")
	var asics []*asic
	for _, w := range []string{"rack1", "rack2"} {
		g.connect(t, "acme."+w, key)
		if _, err := g.ds.Act(g.device(t, c, w).ID, devices.ActApprove, "", operator); err != nil {
			t.Fatal(err)
		}
		a, r := g.connect(t, "acme."+w, key)
		if r["result"] != "true" {
			t.Fatal(r)
		}
		asics = append(asics, a)
	}
	g.connect(t, "globex.rack1", okey)
	if _, err := g.ds.Act(g.device(t, o, "rack1").ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}
	keep, r := g.connect(t, "globex.rack1", okey)
	if r["result"] != "true" {
		t.Fatal(r)
	}

	if _, err := g.ds.SetCustomerStatus(c.ID, devices.CustomerSuspended, "unpaid", operator); err != nil {
		t.Fatal(err)
	}
	for i, a := range asics {
		if !isClosed(a, 2*time.Second) {
			t.Fatalf("device %d of the suspended customer stayed connected", i)
		}
	}
	if r := keep.submit(30, "globex.rack1"); r["result"] != "true" {
		t.Fatalf("another customer was cut: %v", r)
	}
	if _, r := g.connect(t, "acme.rack1", key); r["result"] != "false" {
		t.Fatalf("a suspended customer logged in: %v", r)
	}
	if _, err := g.ds.SetCustomerStatus(c.ID, devices.CustomerActive, "", operator); err != nil {
		t.Fatal(err)
	}
	if _, r := g.connect(t, "acme.rack1", key); r["result"] != "true" {
		t.Fatalf("a reactivated customer was refused: %v", r)
	}
}

func TestWrongKeyAndForeignLoginsNeverReachThePool(t *testing.T) {
	fp := newFakePool(t)
	g := startGated(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	g.connect(t, "acme.rack1", key)
	if _, err := g.ds.Act(g.device(t, c, "rack1").ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ login, key string }{
		{"acme.rack1", "not-the-key"},
		{"acme.rack1", ""},
		{"nobody.rack1", key},
	} {
		a, r := g.connect(t, tc.login, tc.key)
		if r["result"] != "false" {
			t.Fatalf("%s with key %q was authorized: %v", tc.login, tc.key, r)
		}
		isClosed(a, time.Second)
	}
	if _, seen := fp.find("mining.authorize"); seen {
		t.Fatal("a refused login reached the pool")
	}

	// An authorized connection cannot submit shares for a worker it never
	// authorized, whatever the ASIC puts in the share.
	a, r := g.connect(t, "acme.rack1", key)
	if r["result"] != "true" {
		t.Fatal(r)
	}
	if r := a.submit(40, "acme.rack9"); r["result"] == "true" {
		t.Fatalf("a share for an unauthorized worker was accepted: %v", r)
	}
}

func TestExpiredDeviceIsCutWhileSilent(t *testing.T) {
	fp := newFakePool(t)
	g := startGated(t, fp)
	c, key := g.customer(t, "acme", "acmebtc")
	g.connect(t, "acme.rack1", key)
	d := g.device(t, c, "rack1")
	if _, err := g.ds.Act(d.ID, devices.ActApprove, "", operator); err != nil {
		t.Fatal(err)
	}
	a, r := g.connect(t, "acme.rack1", key)
	if r["result"] != "true" {
		t.Fatal(r)
	}

	soon := time.Now().Add(1500 * time.Millisecond).UTC()
	if _, err := g.ds.UpdateDevice(d.ID, devices.DeviceUpdate{ExpiresAt: &soon}, operator); err != nil {
		t.Fatal(err)
	}
	// No share is sent: the periodic check has to find it.
	time.Sleep(1700 * time.Millisecond)
	if n := g.enf.Sweep(); n != 1 {
		t.Fatalf("the sweep closed %d connections, want 1", n)
	}
	if !isClosed(a, 2*time.Second) {
		t.Fatal("an expired device stayed connected")
	}
	if _, r := g.connect(t, "acme.rack1", key); r["result"] != "false" {
		t.Fatalf("an expired device logged in: %v", r)
	}
	got, _ := g.aud.Query(audit.Query{Action: devices.EvDeviceExpired, Limit: 5})
	if len(got) != 1 {
		t.Fatalf("%d expiry entries in the audit log, want exactly 1", len(got))
	}
}
