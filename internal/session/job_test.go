package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

const usableJob = `{"id":null,"method":"mining.notify","params":["job1","prev","c1","c2",[],"20000000","1d00ffff","00000000",true]}`

func jobSession(t *testing.T) (*Session, net.Conn, net.Conn) {
	t.Helper()
	miner, minerPeer := net.Pipe()
	upstream, poolPeer := net.Pipe()
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		miner: miner, up: &Upstream{Conn: &recordingConn{Conn: upstream}, PoolID: "p", PoolName: "Pool"},
		srv:         &Server{Settings: set, Events: events.New(100), Stats: stats.NewCollector()},
		pendingAuth: map[string]authInfo{}, pendingSubmit: map[string]submitInfo{},
		lastMinerActivity: time.Now(), lastPoolActivity: time.Now(),
	}
	t.Cleanup(func() { s.Close("test end"); minerPeer.Close(); poolPeer.Close() })
	return s, minerPeer, poolPeer
}

func TestRejectedAuthorizationIsForwardedBeforeReporting(t *testing.T) {
	for _, result := range []string{"false", "true"} {
		t.Run(result, func(t *testing.T) {
			s, miner, pool := jobSession(t)
			s.pendingAuth["7"] = authInfo{at: time.Now(), user: "account.rig1"}
			failure := make(chan UpstreamFailure, 1)
			s.up.OnFailure = func(kind UpstreamFailure, err error) { failure <- kind }
			done := make(chan error, 1)
			go func() { done <- s.poolLoop(stratum.NewLineReader(s.up.Conn, 65536)) }()
			response := `{"id":"7","result":` + result + `,"error":[24,"miner still banned",null]}` + "\n"
			if _, err := io.WriteString(pool, response); err != nil {
				t.Fatal(err)
			}
			select {
			case <-failure:
				t.Fatal("failure was reported before the response reached the miner")
			default:
			}
			miner.SetReadDeadline(time.Now().Add(time.Second))
			got, err := bufio.NewReader(miner).ReadString('\n')
			if err != nil || got != response {
				t.Fatalf("authorization reply = %q, %v", got, err)
			}
			select {
			case kind := <-failure:
				if kind != AuthorizationRejected {
					t.Fatalf("failure kind = %s", kind)
				}
			case <-time.After(time.Second):
				t.Fatal("authorization failure was not reported")
			}
			var upstreamErr *upstreamError
			if err := <-done; !errors.As(err, &upstreamErr) || !s.authorizedAt.IsZero() {
				t.Fatalf("rejected authorization enabled jobs: %v", err)
			}
		})
	}
}

func TestOnlyUsableJobsAndFirstAuthorizationSetJobDeadline(t *testing.T) {
	s, _, _ := jobSession(t)
	// A job received early must not start the watchdog before authorization.
	if _, err := s.fromPool([]byte(usableJob)); err != nil {
		t.Fatal(err)
	}
	if s.jobTimedOut(time.Now().Add(time.Hour)) {
		t.Fatal("watchdog enabled before authorization")
	}
	s.pendingAuth["1"] = authInfo{user: "account.rig1"}
	if _, err := s.fromPool([]byte(`{"id":1,"result":true,"error":null}`)); err != nil {
		t.Fatal(err)
	}
	authorizedAt, lastJob := s.authorizedAt, s.lastJob
	for _, line := range []string{
		`{"method":"mining.set_difficulty","params":[2048]}`,
		`{"method":"mining.notify","params":["short-job"]}`,
		`{"method":"mining.notify","params":["","prev","c1","c2",[],"v","b","t",true]}`,
		`{"method":"mining.notify","params":[null,"prev","c1","c2",[],"v","b","t",true]}`,
	} {
		if _, err := s.fromPool([]byte(line)); err != nil {
			t.Fatal(err)
		}
		if s.lastJob != lastJob {
			t.Fatalf("non-job extended the deadline: %s", line)
		}
	}
	s.pendingAuth["2"] = authInfo{user: "another.worker"}
	s.fromPool([]byte(`{"id":2,"result":true,"error":null}`))
	if s.authorizedAt != authorizedAt {
		t.Fatal("repeat authorization postponed the first job deadline")
	}
	if !s.jobTimedOut(authorizedAt.Add(time.Minute)) {
		t.Fatal("first job timeout was not enforced")
	}
	if _, err := s.fromPool([]byte(usableJob)); err != nil {
		t.Fatal(err)
	}
	if s.lastJob.Before(authorizedAt) || !s.jobTimedOut(s.lastJob.Add(time.Minute)) {
		t.Fatal("usable job did not set the next job deadline")
	}
	v := settings.Defaults()
	v.UpstreamJobTimeout = 4 * time.Minute
	s.refreshIdleDeadlines(v)
	if got := s.up.Conn.(*recordingConn).deadline(); !got.Equal(s.lastJob.Add(v.UpstreamJobTimeout)) {
		t.Fatalf("updated deadline = %s", got)
	}
}

func TestLiveJobTimeoutChangeInterruptsBusyAndSilentPool(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%v", busy), func(t *testing.T) {
			s, miner, pool := jobSession(t)
			s.authorizedAt = time.Now().Add(-20 * time.Second)
			s.srv.Settings.OnChange(s.refreshIdleDeadlines)
			failure := make(chan UpstreamFailure, 1)
			s.up.OnFailure = func(kind UpstreamFailure, err error) { failure <- kind }
			done := make(chan error, 1)
			go func() { done <- s.poolLoop(stratum.NewLineReader(s.up.Conn, 65536)) }()
			if busy {
				go io.Copy(io.Discard, miner)
				go func() {
					for {
						if _, err := io.WriteString(pool, `{"method":"mining.set_difficulty","params":[1024]}`+"\n"); err != nil {
							return
						}
						time.Sleep(time.Millisecond)
					}
				}()
			}
			deadline := time.Now().Add(time.Second)
			for s.up.Conn.(*recordingConn).deadline().IsZero() {
				if time.Now().After(deadline) {
					t.Fatal("pool reader did not start")
				}
				time.Sleep(time.Millisecond)
			}
			pending, err := s.srv.Settings.Prepare(map[string]json.RawMessage{"upstream_job_timeout": json.RawMessage(`"10s"`)})
			if err != nil {
				t.Fatal(err)
			}
			s.srv.Settings.Commit(pending)
			select {
			case kind := <-failure:
				if kind != JobTimeout {
					t.Fatalf("failure kind = %s", kind)
				}
			case <-time.After(time.Second):
				t.Fatal("changing the timeout did not interrupt the existing pool read")
			}
			if err := <-done; err == nil {
				t.Fatal("timed-out pool loop kept relaying")
			}
		})
	}
}

func TestOperatorCloseDoesNotReportPoolFailure(t *testing.T) {
	s, _, _ := jobSession(t)
	s.up.OnFailure = func(UpstreamFailure, error) { t.Error("operator close reported as pool failure") }
	s.Close("drain: manual switch")
	s.reportFailure(&upstreamError{kind: JobTimeout, err: errors.New("no jobs")})
}

func TestEqualPoolTimeoutsReportJobFailure(t *testing.T) {
	s, _, _ := jobSession(t)
	pending, err := s.srv.Settings.Prepare(map[string]json.RawMessage{"upstream_idle_timeout": json.RawMessage(`"1m"`)})
	if err != nil {
		t.Fatal(err)
	}
	s.srv.Settings.Commit(pending)
	// Activity is recorded before parsing the successful authorization.
	// Even when its idle deadline expires first, the job watchdog must
	// report the silent pool to the manager rather than silently reconnect.
	s.lastPoolActivity = time.Now().Add(-time.Minute)
	s.authorizedAt = s.lastPoolActivity.Add(50 * time.Millisecond)
	failure := make(chan UpstreamFailure, 1)
	s.up.OnFailure = func(kind UpstreamFailure, err error) { failure <- kind }
	done := make(chan error, 1)
	go func() { done <- s.poolLoop(stratum.NewLineReader(s.up.Conn, 65536)) }()
	select {
	case err := <-done:
		var upstreamErr *upstreamError
		if !errors.As(err, &upstreamErr) || upstreamErr.kind != JobTimeout {
			t.Fatalf("equal timeouts did not report a job failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("silent pool did not time out")
	}
	select {
	case kind := <-failure:
		if kind != JobTimeout {
			t.Fatalf("failure kind = %s", kind)
		}
	default:
		t.Fatal("silent pool failure was not reported to the manager")
	}
}
