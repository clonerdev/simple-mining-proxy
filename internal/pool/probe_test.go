package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

func configureCheckPool(t *testing.T, m *Manager, a state.Address) state.Pool {
	t.Helper()
	as := []state.Address{a}
	p, _, err := m.Update("a", Input{Addresses: &as}, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManualHealthRecoveryAndUnsavedCheck(t *testing.T) {
	var banned atomic.Bool
	banned.Store(true)
	a := checkAddress(t, func(c net.Conn) {
		if banned.Load() {
			answerCheck(c, "none", "false", `[24,"miner still banned",null]`)
		} else {
			answerCheck(c, "after_authorize", "true", "null")
		}
	})
	m := testManager(t)
	p := configureCheckPool(t, m, a)
	for i := 0; i < 2; i++ {
		if _, err := m.TestConfig(context.Background(), "a", Input{}); err == nil {
			t.Fatal("unsaved banned configuration passed")
		}
	}
	if h := m.Health("a"); h.Status != StatusUnknown {
		t.Fatalf("editor check changed health: %+v", h)
	}
	for i := 0; i < 2; i++ {
		if _, err := m.TestPool(context.Background(), "a"); err == nil {
			t.Fatal("banned saved pool passed")
		}
		if i == 0 && m.Health("a").Status == StatusDown {
			t.Fatal("one failed check excluded the address")
		}
	}
	if m.Health("a").Status != StatusDown || m.Effective() != "b" {
		t.Fatal("two failed full checks did not select fallback")
	}
	m.dialResult(p, a, nil)
	if m.Health("a").Status != StatusDown {
		t.Fatal("a successful TCP connect restored health")
	}
	banned.Store(false)
	for i := 0; i < 2; i++ {
		if _, err := m.TestPool(context.Background(), "a"); err != nil {
			t.Fatal(err)
		}
		if i == 0 && m.Health("a").Status != StatusDown {
			t.Fatal("one good check restored health")
		}
	}
	if h := m.Health("a"); h.Status != StatusUp || h.UpSince.IsZero() {
		t.Fatalf("full checks did not confirm recovery: %+v", h)
	}
}

func TestRuntimeFailuresConfirmAuthorizationButExcludeStalledAddress(t *testing.T) {
	m, p := multiManager(t)
	a := p.Addresses[0]
	for _, addr := range p.Addresses {
		for i := 0; i < 2; i++ {
			m.probeResult(p, addr, time.Millisecond, nil)
		}
	}
	m.sessionFailure(p, a, session.AuthorizationRejected, errors.New("worker rejected"))
	if m.Health(p.ID).Status != StatusUp || m.AddressHealth(p)[0].Status != StatusUp {
		t.Fatal("one worker rejection excluded a healthy address")
	}
	version := m.checkVersion(p.ID)
	m.sessionFailure(p, a, session.JobTimeout, errors.New("no jobs"))
	if m.AddressHealth(p)[0].Status != StatusDown || m.Health(p.ID).Status != StatusUp {
		t.Fatal("a stalled address must be DOWN while another address keeps its pool UP")
	}
	m.recordCheck(context.Background(), p, a, m.settings.Get().TestWorker, version, time.Millisecond, nil)
	if m.AddressHealth(p)[0].Status != StatusDown {
		t.Fatal("in-flight check overwrote a newer session failure")
	}
	if got := order(m, p); len(got) != 2 || got[0] == a.Host {
		t.Fatalf("stalled address still selected: %v", got)
	}
}

func TestHealthIgnoresCancelledAndOldConfigurationChecks(t *testing.T) {
	for _, change := range []string{"username", "password", "test_worker"} {
		t.Run(change, func(t *testing.T) {
			m := testManager(t)
			p, _ := m.Snapshot().Get("a")
			a, worker, version := p.Addresses[0], m.settings.Get().TestWorker, m.checkVersion(p.ID)
			for i := 0; i < 2; i++ {
				m.probeResult(p, a, time.Millisecond, nil)
			}
			switch change {
			case "username":
				u := "new.{worker}"
				if _, _, err := m.Update(p.ID, Input{Username: &u}, false); err != nil {
					t.Fatal(err)
				}
			case "password":
				password := "new-password"
				if _, _, err := m.Update(p.ID, Input{Password: &password}, false); err != nil {
					t.Fatal(err)
				}
			case "test_worker":
				pending, err := m.settings.Prepare(map[string]json.RawMessage{"test_worker": json.RawMessage(`"newworker"`)})
				if err != nil {
					t.Fatal(err)
				}
				m.settings.Commit(pending)
			}
			for i := 0; i < 2; i++ {
				m.recordCheck(context.Background(), p, a, worker, version, 0, errors.New("old credentials"))
			}
			if h := m.Health(p.ID); h.Status != StatusUnknown || h.LastError != "" {
				t.Fatalf("old configuration check applied: %+v", h)
			}
			cur, _ := m.Snapshot().Get(p.ID)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			m.recordCheck(ctx, cur, a, m.settings.Get().TestWorker, m.checkVersion(p.ID), 0, errors.New("cancelled"))
			if !m.Health(p.ID).LastCheck.IsZero() {
				t.Fatal("cancelled check changed health")
			}
		})
	}
}

func TestBackgroundChecksCoalesceAndRespectRateLimit(t *testing.T) {
	var checks atomic.Int32
	a := checkAddress(t, func(c net.Conn) {
		checks.Add(1)
		answerCheck(c, "after_authorize", "true", "null")
	})
	m := testManager(t)
	p := configureCheckPool(t, m, a)
	for i := 0; i < 50; i++ {
		m.requestProbe(p.ID, a)
		m.probeRequested(context.Background())
	}
	m.probeWG.Wait()
	if checks.Load() != 1 || m.AddressHealth(p)[0].LastCheck.IsZero() {
		t.Fatalf("background check count = %d", checks.Load())
	}
	entry := m.probeEntry(addrKey(p.ID, a))
	<-entry.gate
	entry.lastStart = time.Now().Add(-recheckInterval)
	entry.gate <- struct{}{}
	m.requestProbe(p.ID, a)
	m.probeRequested(context.Background())
	m.probeWG.Wait()
	if checks.Load() != 2 || m.Health(p.ID).Status != StatusUp {
		t.Fatal("full background checks did not confirm a working pool")
	}
}

func TestJobTimeoutSettingDefaultsAndLimits(t *testing.T) {
	if settings.Defaults().UpstreamJobTimeout != time.Minute {
		t.Fatal("job timeout default is not one minute")
	}
	m := testManager(t)
	for _, value := range []string{`"9s"`, `"61m"`} {
		if _, err := m.settings.Prepare(map[string]json.RawMessage{"upstream_job_timeout": json.RawMessage(value)}); err == nil {
			t.Fatalf("invalid job timeout %s accepted", value)
		}
	}
}

func TestEditingPoolDuringCheckDiscardsResultAndPreventsActivation(t *testing.T) {
	for _, operation := range []string{"test", "activate"} {
		t.Run(operation, func(t *testing.T) {
			started, release := make(chan struct{}, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			a := checkAddress(t, func(c net.Conn) {
				lr := stratum.NewLineReader(c, 65536)
				if _, err := lr.ReadLine(); err != nil {
					return
				}
				fmt.Fprintln(c, `{"id":1,"result":[[],"extra",4],"error":null}`)
				if _, err := lr.ReadLine(); err != nil {
					return
				}
				started <- struct{}{}
				<-release
				fmt.Fprintln(c, `{"id":2,"result":true,"error":null}`)
				fmt.Fprintln(c, testJob)
				io.Copy(io.Discard, c)
			})
			t.Cleanup(unblock)
			m := testManager(t)
			addresses := []state.Address{a}
			if _, _, err := m.Update("b", Input{Addresses: &addresses}, false); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if operation == "test" {
					_, err := m.TestPool(context.Background(), "b")
					done <- err
				} else {
					_, err := m.Activate(context.Background(), "b", false)
					done <- err
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("check did not reach authorization")
			}
			username := "changed.{worker}"
			if _, _, err := m.Update("b", Input{Username: &username}, false); err != nil {
				t.Fatal(err)
			}
			unblock()
			select {
			case err := <-done:
				if operation == "activate" && err == nil {
					t.Fatal("activation used a check of the old pool configuration")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("check deadlocked with the pool edit")
			}
			if h := m.Health("b"); h.Status != StatusUnknown || !h.LastCheck.IsZero() || m.Snapshot().Active != "a" {
				t.Fatalf("old check affected the edited pool: %+v", h)
			}
		})
	}
}

func TestPersistedWorkerChangeDoesNotWaitForPoolOperationLock(t *testing.T) {
	m := testManager(t)
	pending, err := m.settings.Prepare(map[string]json.RawMessage{"test_worker": json.RawMessage(`"changed"`)})
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent pool operation owns opMu and may need the state-store
	// lock. The settings commit owns that store lock and must finish without
	// waiting for opMu, otherwise the two operations deadlock.
	m.opMu.Lock()
	defer m.opMu.Unlock()
	done := make(chan error, 1)
	go func() {
		done <- m.st.Mutate(func(f *state.File) error {
			f.Settings = pending.Overrides
			return nil
		}, func(*state.File) { m.settings.Commit(pending) })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("settings commit deadlocked with a pool operation")
	}
}
