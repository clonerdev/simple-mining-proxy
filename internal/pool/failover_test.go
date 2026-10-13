package pool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
)

type connectedRouter struct{ upstreams chan *session.Upstream }

func (r *connectedRouter) Pick(ctx context.Context) (*session.Upstream, error) {
	select {
	case up := <-r.upstreams:
		return up, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Open actual relay sessions without DNS or real-time recovery waits.
func sessionFixture(t *testing.T, m *Manager) func(string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	router := &connectedRouter{upstreams: make(chan *session.Upstream, 1)}
	srv := &session.Server{Settings: m.settings, Router: router, Registry: m.reg, Events: m.ev, Stats: stats.NewCollector()}
	var peers []net.Conn
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		m.reg.CloseAll("test end")
		for _, peer := range peers {
			peer.Close()
		}
		wg.Wait()
	})
	return func(id string) {
		t.Helper()
		p, _ := m.Snapshot().Get(id)
		miner, minerPeer := net.Pipe()
		up, poolPeer := net.Pipe()
		peers = append(peers, minerPeer, poolPeer)
		router.upstreams <- &session.Upstream{Conn: up, PoolID: id, PoolName: p.Name, Addr: p.Addresses[0].String()}
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(io.Discard, poolPeer) }()
		go func() { defer wg.Done(); srv.Serve(ctx, miner, false, func() {}) }()
		before := m.reg.Counts().Total
		if _, err := io.WriteString(minerPeer, `{"id":1,"method":"mining.subscribe","params":[]}`+"\n"); err != nil {
			t.Fatal(err)
		}
		waitSessionCount(t, m, before+1)
	}
}

func waitSessionCount(t *testing.T, m *Manager, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.reg.Counts().Total != count {
		if time.Now().After(deadline) {
			t.Fatalf("session count = %d, want %d", m.reg.Counts().Total, count)
		}
		time.Sleep(time.Millisecond)
	}
}

func setDrain(t *testing.T, m *Manager, value string) {
	t.Helper()
	pending, err := m.settings.Prepare(map[string]json.RawMessage{"switch_drain": json.RawMessage(value)})
	if err != nil {
		t.Fatal(err)
	}
	m.settings.Commit(pending)
}

func TestFailbackWaitsForConfirmedRecoveryAndDelay(t *testing.T) {
	m := testManager(t)
	setDrain(t, m, `"0s"`)
	p, _ := m.Snapshot().Get("a")
	a := p.Addresses[0]
	for i := 0; i < 2; i++ {
		m.probeResult(p, a, 0, errors.New("banned"))
	}
	connect := sessionFixture(t, m)
	connect("b")
	m.probeResult(p, a, time.Millisecond, nil)
	m.checkFailback(time.Now().Add(time.Hour))
	if m.reg.Counts().Total != 1 {
		t.Fatal("one good check initiated failback")
	}
	m.probeResult(p, a, time.Millisecond, nil)
	up := m.Health("a").UpSince
	delay := m.settings.Get().FailbackDelay
	m.checkFailback(up.Add(delay - time.Nanosecond))
	if m.reg.Counts().Total != 1 {
		t.Fatal("failback occurred before its delay")
	}
	m.checkFailback(up.Add(delay))
	waitSessionCount(t, m, 0)
}

func TestFailoverDoesNotRestartDrainOrOverrideManualSwitch(t *testing.T) {
	m := testManager(t)
	setDrain(t, m, `"300ms"`)
	connect := sessionFixture(t, m)
	connect("a")
	connect("a")
	p, _ := m.Snapshot().Get("a")
	for i := 0; i < 2; i++ {
		m.probeResult(p, p.Addresses[0], 0, errors.New("miner still banned"))
	}
	m.checkFailover()
	for i := 0; i < 20; i++ {
		m.checkFailover()
	}
	if _, err := m.Activate(context.Background(), "b", true); err != nil {
		t.Fatal(err)
	}
	m.checkFailover()
	waitSessionCount(t, m, 0)
	count := 0
	for _, event := range m.ev.List(0) {
		if event.Type == "failover" {
			count++
		}
	}
	if count != 1 || m.Snapshot().Active != "b" {
		t.Fatalf("failover restarted or overrode manual activation: events=%d, active=%s", count, m.Snapshot().Active)
	}
}
