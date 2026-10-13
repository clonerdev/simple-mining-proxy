package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

func runPoolManager(t *testing.T, px *proxy) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); px.mgr.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitForDuration(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func setBanned(p *fakePool, banned bool) {
	p.mu.Lock()
	p.banned = banned
	p.mu.Unlock()
}

func addAddress(t *testing.T, px *proxy, other *fakePool) {
	t.Helper()
	p, _ := px.mgr.Snapshot().Get("pool_a")
	addresses := append(append([]state.Address(nil), p.Addresses...), state.Address{Host: "127.0.0.1", Port: other.port()})
	if _, _, err := px.mgr.Update(p.ID, pool.Input{Addresses: &addresses}, false); err != nil {
		t.Fatal(err)
	}
}

func TestBannedAddressesAutomaticallyDrainToFallbackAndNextFallback(t *testing.T) {
	primary, other, backup, last := newFakePool(t), newFakePool(t), newFakePool(t), newFakePool(t)
	px := startProxy(t, primary, backup, last)
	addAddress(t, px, other)
	if err := px.mgr.SetFallback([]string{"pool_b", "pool_c"}); err != nil {
		t.Fatal(err)
	}
	pending, err := px.set.Prepare(map[string]json.RawMessage{"pool_probe_interval": json.RawMessage(`"10s"`)})
	if err != nil {
		t.Fatal(err)
	}
	px.set.Commit(pending)
	m := dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	setBanned(primary, true)
	setBanned(other, true)
	runPoolManager(t, px)
	waitForDuration(t, 15*time.Second, "background ban detection and drain", func() bool {
		return px.mgr.Health("pool_a").Status == pool.StatusDown && px.reg.Counts().Total == 0
	})
	m = dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	if px.reg.Counts().ByPool["pool_b"] != 1 {
		t.Fatal("banned primary did not route to the first fallback")
	}
	setBanned(backup, true)
	for i := 0; i < 2; i++ {
		if _, err := px.mgr.TestPool(context.Background(), "pool_b"); err == nil {
			t.Fatal("banned fallback passed its test")
		}
	}
	waitFor(t, "failed fallback session drain", func() bool { return px.reg.Counts().Total == 0 })
	m = dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	if px.reg.Counts().ByPool["pool_c"] != 1 {
		t.Fatal("failed fallback did not route to the next fallback")
	}
	if s := px.mgr.Snapshot(); s.Active != "pool_a" || strings.Join(s.Fallback, ",") != "pool_b,pool_c" {
		t.Fatalf("failover changed configured roles: %+v", s)
	}
	found := false
	for _, event := range px.ev.List(0) {
		found = found || event.Type == "failover" && strings.Contains(event.Message, "miner still banned") && strings.Contains(event.Message, "reconnecting 1 sessions")
	}
	if !found {
		t.Fatal("no failover event with the pool error and reconnect count")
	}
}

func TestFailedAddressDrainsWithinSamePool(t *testing.T) {
	primary, other, backup := newFakePool(t), newFakePool(t), newFakePool(t)
	px := startProxy(t, primary, backup)
	addAddress(t, px, other)
	if err := px.mgr.SetFallback([]string{"pool_b"}); err != nil {
		t.Fatal(err)
	}
	m := dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	setBanned(primary, true)
	for i := 0; i < 2; i++ {
		if _, err := px.mgr.TestPool(context.Background(), "pool_a"); err != nil {
			t.Fatalf("healthy second address should keep pool available: %v", err)
		}
	}
	runPoolManager(t, px)
	waitFor(t, "failed address session drain", func() bool { return px.reg.Counts().Total == 0 })
	m = dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	infos := px.reg.Infos(time.Now())
	if len(infos) != 1 || infos[0].PoolID != "pool_a" || infos[0].PoolAddr != other.ln.Addr().String() {
		t.Fatalf("miner should move to the healthy address of the same pool: %+v", infos)
	}
}

func TestSingleWorkerRejectionKeepsPoolAvailable(t *testing.T) {
	primary, backup := newFakePool(t), newFakePool(t)
	primary.mu.Lock()
	primary.rejectUser = "accountA.badworker"
	primary.mu.Unlock()
	px := startProxy(t, primary, backup)
	runPoolManager(t, px)
	m := dialASIC(t, px.addr)
	m.send(`{"id":1,"method":"mining.subscribe","params":[]}`)
	m.response(1)
	m.send(`{"id":2,"method":"mining.authorize","params":["farm.badworker","x"]}`)
	if r := m.response(2); string(r["result"]) != "false" || !strings.Contains(string(r["error"]), "miner still banned") {
		t.Fatalf("miner did not receive rejection: %v", r)
	}
	waitFor(t, "rejected worker session close and full check", func() bool {
		return px.reg.Counts().Total == 0 && !px.mgr.Health("pool_a").LastCheck.IsZero()
	})
	if px.mgr.Health("pool_a").Status == pool.StatusDown || px.mgr.Effective() != "pool_a" {
		t.Fatal("one rejected worker excluded the address despite a successful pool check")
	}
	m = dialASIC(t, px.addr)
	m.handshake("farm.goodworker")
	if px.reg.Counts().ByPool["pool_a"] != 1 {
		t.Fatal("healthy worker was moved away from its pool")
	}
}

func TestSessionJobTimeoutRoutesToFallbackDespiteSuccessfulProbe(t *testing.T) {
	primary, backup := newFakePool(t), newFakePool(t)
	px := startProxy(t, primary, backup)
	if err := px.mgr.SetFallback([]string{"pool_b"}); err != nil {
		t.Fatal(err)
	}
	pending, err := px.set.Prepare(map[string]json.RawMessage{"upstream_job_timeout": json.RawMessage(`"10s"`)})
	if err != nil {
		t.Fatal(err)
	}
	px.set.Commit(pending)
	m := dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	// The pool keeps this existing connection alive with difficulty messages,
	// while fresh checks still get an initial job. The session watchdog must
	// exclude it independently of those successful probes.
	for i := 0; i < 2; i++ {
		if _, err := px.mgr.TestPool(context.Background(), "pool_a"); err != nil {
			t.Fatal(err)
		}
	}
	stalled := m
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if _, err := stalled.conn.Write([]byte(`{"method":"test.keepalive","params":[]}` + "\n")); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { stalled.conn.Close(); <-done })
	m.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	for {
		if _, err := m.r.ReadBytes('\n'); err != nil {
			break
		}
	}
	if px.mgr.Health("pool_a").Status != pool.StatusDown {
		t.Fatal("job watchdog did not exclude the busy address")
	}
	waitFor(t, "stalled miner session close", func() bool { return px.reg.Counts().Total == 0 })
	m = dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	if px.reg.Counts().ByPool["pool_b"] != 1 {
		t.Fatal("stalled miner did not reconnect to fallback")
	}
}
