package pool

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// fakeOperatorPool answers subscribe and the first authorize like a pool, and
// the operator's authorize (id "bitlite-op") by mode.
func fakeOperatorPool(t *testing.T, mode string) state.Address {
	return checkAddress(t, func(c net.Conn) {
		lr := stratum.NewLineReader(c, 65536)
		for {
			line, err := lr.ReadLine()
			if err != nil {
				return
			}
			msg, err := stratum.Parse(line)
			if err != nil {
				return
			}
			switch {
			case msg.Method == "mining.subscribe":
				fmt.Fprintf(c, `{"id":%s,"result":[[],"extra",4],"error":null}`+"\n", msg.ID)
			case msg.Method == "mining.authorize" && strings.Contains(string(msg.ID), "bitlite-op"):
				switch mode {
				case "accept":
					fmt.Fprintf(c, `{"id":%s,"result":true,"error":null}`+"\n", msg.ID)
				case "accept_then_close":
					fmt.Fprintf(c, `{"id":%s,"result":true,"error":null}`+"\n", msg.ID)
					return
				case "refuse":
					fmt.Fprintf(c, `{"id":%s,"result":false,"error":[24,"unauthorized worker",null]}`+"\n", msg.ID)
				case "close":
					return
				case "silent":
				}
			case msg.Method == "mining.authorize":
				fmt.Fprintf(c, `{"id":%s,"result":true,"error":null}`+"\n", msg.ID)
				fmt.Fprintln(c, testJob)
			}
		}
	})
}

func probe(t *testing.T, mode string, user string) OperatorResult {
	t.Helper()
	a := fakeOperatorPool(t, mode)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := state.Pool{Username: "acct.{worker}", Password: "x", OperatorShare: true, OperatorUser: user, OperatorPassword: "x"}
	return CheckOperator(ctx, p, a, "proxytest")
}

func stepState(r OperatorResult, id string) string {
	for _, s := range r.Steps {
		if s.ID == id {
			switch {
			case s.Skipped:
				return "skipped"
			case s.OK:
				return "ok"
			}
			return "failed"
		}
	}
	return "absent"
}

func TestOperatorProbeVerdicts(t *testing.T) {
	for _, tc := range []struct {
		mode, verdict, auth, open string
		slow                      bool
	}{
		{"accept", VerdictAuthorizeAccepted, "ok", "ok", false},
		{"accept_then_close", VerdictClosed, "ok", "failed", false},
		{"refuse", VerdictRefused, "failed", "skipped", false},
		{"close", VerdictClosed, "failed", "skipped", false},
		{"silent", VerdictNoAnswer, "failed", "skipped", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if tc.slow && testing.Short() {
				t.Skip("waits for the answer timeout")
			}
			r := probe(t, tc.mode, "operator_acct")
			if r.Verdict != tc.verdict {
				t.Fatalf("verdict = %q, want %q (%+v)", r.Verdict, tc.verdict, r.Steps)
			}
			for _, id := range []string{StepConnect, StepSubscribe, StepAuthorize, StepJob} {
				if got := stepState(r, id); got != "ok" {
					t.Errorf("%s = %s, want ok", id, got)
				}
			}
			if got := stepState(r, StepOperatorAuth); got != tc.auth {
				t.Errorf("operator authorize = %s, want %s", got, tc.auth)
			}
			if got := stepState(r, StepConnectionOpen); got != tc.open {
				t.Errorf("connection open = %s, want %s", got, tc.open)
			}
			if r.Verdict == VerdictRefused {
				var d string
				for _, s := range r.Steps {
					if s.ID == StepOperatorAuth {
						d = s.Detail
					}
				}
				if !strings.Contains(d, "unauthorized worker") {
					t.Errorf("the pool's reason is lost: %q", d)
				}
			}
		})
	}
}

func TestOperatorProbeNeverReachesTheQuestionWhenTheCustomerLoginFails(t *testing.T) {
	a := checkAddress(t, func(c net.Conn) { answerCheck(c, "none", "false", "null") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := CheckOperator(ctx, state.Pool{Username: "acct.{worker}", OperatorUser: "op"}, a, "proxytest")
	if r.Verdict != VerdictNotReached || stepState(r, StepAuthorize) != "failed" || stepState(r, StepOperatorAuth) != "skipped" {
		t.Fatalf("got %q %+v", r.Verdict, r.Steps)
	}
}

func TestOperatorProbeNeedsAnOperatorLogin(t *testing.T) {
	r := probe(t, "accept", "")
	if r.Verdict != VerdictNotReached || stepState(r, StepConnect) != "failed" {
		t.Fatalf("got %q %+v", r.Verdict, r.Steps)
	}
}

func TestOperatorProbeUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := CheckOperator(ctx, state.Pool{Username: "a", OperatorUser: "op"}, state.Address{Host: "127.0.0.1", Port: 1}, "t")
	if r.Verdict != VerdictNotReached || stepState(r, StepConnect) != "failed" || stepState(r, StepSubscribe) != "skipped" {
		t.Fatalf("got %q %+v", r.Verdict, r.Steps)
	}
}
