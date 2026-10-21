package pool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// Steps of the operator probe, in the order they are tried.
const (
	StepConnect        = "connect"
	StepSubscribe      = "subscribe"
	StepAuthorize      = "authorize"
	StepJob            = "job"
	StepOperatorAuth   = "operator_authorize"
	StepConnectionOpen = "connection_open"
)

// Verdicts of the operator probe for one address.
const (
	// VerdictAuthorizeAccepted: the pool accepted the second authorize and kept
	// the connection. It does NOT say the pool credits shares to that worker;
	// that can only be seen on the pool's own dashboard.
	VerdictAuthorizeAccepted = "authorize_accepted"
	VerdictRefused           = "refused"
	VerdictNoAnswer          = "no_answer"
	VerdictClosed            = "closed"
	// VerdictNotReached: a step before the second authorize failed, so the
	// question was not asked at all.
	VerdictNotReached = "not_reached"
)

const (
	operatorAnswerWait = 6 * time.Second
	openCheckWait      = 1500 * time.Millisecond
	operatorProbeID    = "bitlite-op"
)

// ProbeStep is the outcome of one step. A step that was not tried because an
// earlier one failed is Skipped.
type ProbeStep struct {
	ID      string
	OK      bool
	Skipped bool
	Detail  string // what the pool said, if anything
}

// OperatorResult is the probe of one address.
type OperatorResult struct {
	Address state.Address
	RTT     time.Duration
	Steps   []ProbeStep
	Verdict string
}

var probeStepOrder = []string{StepConnect, StepSubscribe, StepAuthorize, StepJob, StepOperatorAuth, StepConnectionOpen}

// failAt marks step id as failed and every later one as skipped.
func (r *OperatorResult) failAt(id, detail, verdict string) {
	r.Verdict = verdict
	seen := false
	for _, s := range probeStepOrder {
		switch {
		case s == id:
			seen = true
			r.Steps = append(r.Steps, ProbeStep{ID: s, Detail: detail})
		case seen:
			r.Steps = append(r.Steps, ProbeStep{ID: s, Skipped: true})
		}
	}
}

// CheckOperator asks one address of a pool the question operator share depends
// on: does it accept a second mining.authorize, for the operator's worker, on
// a connection that already authorized the customer's worker, and keep the
// connection? The ask is the same one a session makes (same request id and
// order). Nothing is mined and no share is submitted, so the probe cannot show
// that shares are credited to the second worker: it says so, and leaves that
// to the pool's dashboard.
func CheckOperator(ctx context.Context, p state.Pool, a state.Address, testWorker string) OperatorResult {
	res := OperatorResult{Address: a}
	if p.OperatorUser == "" {
		res.failAt(StepConnect, "no operator login", VerdictNotReached)
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	conn, rtt, err := Dial(ctx, p, a, checkTimeout)
	if err != nil {
		res.failAt(StepConnect, err.Error(), VerdictNotReached)
		return res
	}
	defer conn.Close()
	res.RTT = rtt
	res.Steps = append(res.Steps, ProbeStep{ID: StepConnect, OK: true})
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	lr := stratum.NewLineReader(conn, 64*1024)
	rw := stratum.RewriteLogin(testWorker, "x", p.Username, p.Password)
	gotJob := false

	if err := probeCall(ctx, conn, lr, 1, "mining.subscribe", []any{userAgent}, false, &gotJob); err != nil {
		res.failAt(StepSubscribe, probeErrText(ctx, err), VerdictNotReached)
		return res
	}
	res.Steps = append(res.Steps, ProbeStep{ID: StepSubscribe, OK: true})
	if err := probeCall(ctx, conn, lr, 2, "mining.authorize", []any{rw.User, rw.Password}, true, &gotJob); err != nil {
		res.failAt(StepAuthorize, probeErrText(ctx, err), VerdictNotReached)
		return res
	}
	res.Steps = append(res.Steps, ProbeStep{ID: StepAuthorize, OK: true})
	for !gotJob {
		line, err := lr.ReadLine()
		if err != nil {
			res.failAt(StepJob, probeErrText(ctx, err), VerdictNotReached)
			return res
		}
		msg, err := stratum.Parse(line)
		if err != nil {
			res.failAt(StepJob, "invalid response", VerdictNotReached)
			return res
		}
		gotJob = stratum.IsJob(msg)
	}
	res.Steps = append(res.Steps, ProbeStep{ID: StepJob, OK: true})

	// The second authorize, exactly as a session sends it.
	req, _ := json.Marshal(struct {
		ID     string   `json:"id"`
		Method string   `json:"method"`
		Params []string `json:"params"`
	}{operatorProbeID, "mining.authorize", []string{p.OperatorUser, p.OperatorPassword}})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		res.failAt(StepOperatorAuth, probeErrText(ctx, err), VerdictClosed)
		return res
	}
	wait := time.Now().Add(operatorAnswerWait)
	if dl, ok := ctx.Deadline(); ok && dl.Before(wait) {
		wait = dl
	}
	_ = conn.SetReadDeadline(wait)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			if isTimeout(err) || ctx.Err() != nil {
				res.failAt(StepOperatorAuth, "no answer to the second authorize", VerdictNoAnswer)
			} else {
				res.failAt(StepOperatorAuth, "the pool closed the connection: "+err.Error(), VerdictClosed)
			}
			return res
		}
		msg, err := stratum.Parse(line)
		if err != nil || msg.Method != "" || msg.IDKey() != `"`+operatorProbeID+`"` {
			continue // a job, a difficulty or a note: not the answer
		}
		if !stratum.IsNull(msg.Error) {
			res.failAt(StepOperatorAuth, "pool error: "+stratum.ErrorReason(msg.Error), VerdictRefused)
			return res
		}
		if !stratum.ResultTrue(msg.Result) {
			res.failAt(StepOperatorAuth, "rejected by pool (result "+string(msg.Result)+")", VerdictRefused)
			return res
		}
		break
	}
	res.Steps = append(res.Steps, ProbeStep{ID: StepOperatorAuth, OK: true})

	// Some pools answer "true" and then drop the connection: that would cost
	// the customer their session, so it is a failure.
	_ = conn.SetReadDeadline(time.Now().Add(openCheckWait))
	for {
		if _, err := lr.ReadLine(); err != nil {
			if isTimeout(err) {
				break // quiet and open: good
			}
			if ctx.Err() != nil {
				break
			}
			res.failAt(StepConnectionOpen, "the pool closed the connection after the second authorize", VerdictClosed)
			return res
		}
	}
	res.Steps = append(res.Steps, ProbeStep{ID: StepConnectionOpen, OK: true})
	res.Verdict = VerdictAuthorizeAccepted
	return res
}

// CheckOperatorAll probes every address of a pool in parallel.
func CheckOperatorAll(ctx context.Context, p state.Pool, testWorker string) []OperatorResult {
	out := make([]OperatorResult, len(p.Addresses))
	done := make(chan struct{}, len(p.Addresses))
	for i, a := range p.Addresses {
		go func() {
			out[i] = CheckOperator(ctx, p, a, testWorker)
			done <- struct{}{}
		}()
	}
	for range p.Addresses {
		<-done
	}
	return out
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

func probeErrText(ctx context.Context, err error) string {
	if ctx.Err() != nil || isTimeout(err) {
		return errNoAnswer.Error()
	}
	if errors.Is(err, io.EOF) {
		return "the pool closed the connection"
	}
	return err.Error()
}

// probeCall is callObserved over one shared line reader, so that nothing the
// pool sent after an answer is lost between steps.
func probeCall(ctx context.Context, conn net.Conn, lr *stratum.LineReader, id int, method string, params []any, wantTrue bool, gotJob *bool) error {
	req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	want := fmt.Sprint(id)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			return err
		}
		msg, err := stratum.Parse(line)
		if err != nil {
			return fmt.Errorf("invalid response: %w", err)
		}
		if stratum.IsJob(msg) {
			*gotJob = true
		}
		if msg.Method != "" || (msg.IDKey() != want && msg.IDKey() != `"`+want+`"`) {
			continue
		}
		if !stratum.IsNull(msg.Error) {
			return errors.New("pool error: " + stratum.ErrorReason(msg.Error))
		}
		if stratum.IsNull(msg.Result) {
			return errors.New("empty result")
		}
		if wantTrue {
			if !stratum.ResultTrue(msg.Result) {
				return errors.New("rejected by pool (result " + string(msg.Result) + ")")
			}
		} else {
			var sub []json.RawMessage
			if err := json.Unmarshal(msg.Result, &sub); err != nil || len(sub) < 3 {
				return errors.New("invalid mining.subscribe result " + string(msg.Result))
			}
		}
		return nil
	}
}
