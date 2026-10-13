package pool

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

const recheckInterval = 10 * time.Second

func (m *Manager) settingsChanged(v *settings.Values) {
	// Settings listeners run inside state.Store.Mutate. Taking opMu here
	// would invert the order used by pool edits (opMu -> state store).
	m.hmu.Lock()
	if v.TestWorker == m.testWorker {
		m.hmu.Unlock()
		return
	}
	m.testWorker = v.TestWorker
	snap := m.Snapshot()
	m.addrs, m.pools = map[string]*addrHealth{}, map[string]*poolHealth{}
	for _, p := range snap.Pools {
		m.versions[p.ID]++
	}
	m.hmu.Unlock()
	for _, p := range snap.Pools {
		for _, a := range p.Addresses {
			m.requestProbe(p.ID, a)
		}
	}
}

// The gate serializes background and saved-pool manual checks. Only its
// holder reads or changes lastStart; pending requests remain queued when busy.
type addressProbe struct {
	gate      chan struct{}
	lastStart time.Time
}

func (m *Manager) probeEntry(key string) *addressProbe {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	entry := m.probes[key]
	if entry == nil {
		entry = &addressProbe{gate: make(chan struct{}, 1)}
		entry.gate <- struct{}{}
		m.probes[key] = entry
	}
	return entry
}

func (m *Manager) requestProbe(id string, a state.Address) {
	m.probeMu.Lock()
	m.probePending[addrKey(id, a)] = true
	m.probeMu.Unlock()
	select {
	case m.probeWake <- struct{}{}:
	default:
	}
}

func (m *Manager) probeRequested(ctx context.Context) {
	m.probeMu.Lock()
	pending := make(map[string]bool, len(m.probePending))
	for key := range m.probePending {
		pending[key] = true
	}
	m.probeMu.Unlock()
	for _, p := range m.Snapshot().Pools {
		for _, a := range p.Addresses {
			if pending[addrKey(p.ID, a)] {
				m.startProbe(ctx, p, a)
			}
		}
	}
}

func (m *Manager) checkVersion(id string) uint64 {
	m.hmu.Lock()
	defer m.hmu.Unlock()
	return m.versions[id]
}

func (m *Manager) startProbe(ctx context.Context, p state.Pool, a state.Address) {
	key := addrKey(p.ID, a)
	entry := m.probeEntry(key)
	select {
	case <-entry.gate:
	default:
		return
	}
	if ctx.Err() != nil || time.Since(entry.lastStart) < recheckInterval {
		entry.gate <- struct{}{}
		return
	}
	m.opMu.Lock()
	worker, version := m.settings.Get().TestWorker, m.checkVersion(p.ID)
	current := m.stillHas(p, a)
	m.opMu.Unlock()
	if !current {
		entry.gate <- struct{}{}
		return
	}
	entry.lastStart = time.Now()
	m.probeMu.Lock()
	delete(m.probePending, key)
	m.probeMu.Unlock()
	m.probeWG.Add(1)
	go func() {
		defer m.probeWG.Done()
		defer func() { entry.gate <- struct{}{} }()
		rtt, err := Check(ctx, p, a, true, worker)
		m.recordCheck(ctx, p, a, worker, version, rtt, err)
	}()
}

func (m *Manager) recordCheck(ctx context.Context, p state.Pool, a state.Address, worker string, version uint64, rtt time.Duration, err error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.probeResultIf(p, a, rtt, err, func() bool {
		return ctx.Err() == nil && m.stillHas(p, a) && m.settings.Get().TestWorker == worker && m.versions[p.ID] == version
	})
}

func (m *Manager) checkSaved(ctx context.Context, p state.Pool) []AddrResult {
	requestCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	m.opMu.Lock()
	worker, version := m.settings.Get().TestWorker, m.checkVersion(p.ID)
	m.opMu.Unlock()
	out := make([]AddrResult, len(p.Addresses))
	var wg sync.WaitGroup
	for i, a := range p.Addresses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry := m.probeEntry(addrKey(p.ID, a))
			select {
			case <-ctx.Done():
				out[i] = AddrResult{Address: a, Err: ctx.Err()}
				return
			case <-entry.gate:
			}
			defer func() { entry.gate <- struct{}{} }()
			if err := ctx.Err(); err != nil {
				out[i] = AddrResult{Address: a, Err: err}
				return
			}
			entry.lastStart = time.Now()
			rtt, err := Check(ctx, p, a, true, worker)
			out[i] = AddrResult{Address: a, RTT: rtt, Err: err}
			// Our own ten-second budget is a pool failure. Cancellation of
			// the caller's request must not affect health.
			m.recordCheck(requestCtx, p, a, worker, version, rtt, err)
		}()
	}
	wg.Wait()
	return out
}

func (m *Manager) sessionFailure(p state.Pool, a state.Address, kind session.UpstreamFailure, err error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !m.stillHas(p, a) {
		return
	}
	if kind == session.JobTimeout {
		m.hmu.Lock()
		h := m.addrEntry(addrKey(p.ID, a))
		prev := h.status
		h.status, h.lastCheck, h.lastError = StatusDown, time.Now(), err.Error()
		h.probeOKs, h.probeFails, h.dialFails = 0, 0, 0
		h.upSince = time.Time{}
		// A check started before the stalled session was detected cannot
		// override the verdict. Recovery needs two subsequent full checks.
		m.versions[p.ID]++
		events := m.addrChangedLocked(p, a, prev, h, h.lastCheck)
		m.hmu.Unlock()
		for _, event := range events {
			event()
		}
	}
	m.requestProbe(p.ID, a)
}

// Failover is a drain of sessions on failed addresses, including sessions
// already on a fallback. Pick preserves address and fallback preference.
func (m *Manager) checkFailover() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.reg.Draining() {
		return
	}
	failed := map[string]bool{}
	var reasons []string
	for _, p := range m.Snapshot().Pools {
		for _, a := range m.AddressHealth(p) {
			if a.Status == StatusDown {
				failed[addrKey(p.ID, a.Address)] = true
				reasons = append(reasons, fmt.Sprintf("%s %s: %s", p.Name, a.Address, a.LastError))
			}
		}
	}
	filter := func(s *session.Session) bool {
		return failed[s.PoolID()+"|"+strings.ToLower(s.PoolAddr())]
	}
	if m.reg.CountWhere(filter) == 0 {
		return
	}
	reason := "failover: " + strings.Join(reasons, "; ")
	n := m.reg.Drain(filter, m.settings.Get().SwitchDrain, reason)
	m.ev.Warn("failover", "%s; reconnecting %d sessions", reason, n)
}
