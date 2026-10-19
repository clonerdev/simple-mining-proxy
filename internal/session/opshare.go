package session

import (
	"encoding/json"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// opAuthKey is the request id of the operator's mining.authorize, in the form
// Message.IDKey gives for a string id. A pool answers it under the same id, and
// the answer is handled here and never reaches the ASIC.
const opAuthKey = `"bitlite-op"`

// splitterOn reports whether the operator's share may be tried at all on this
// session: the proxy has a Splitter, its switch is on and the pool was marked
// as one that credits a second worker.
func (s *Session) splitterOn() bool {
	return s.srv.Splitter != nil && s.srv.Splitter.Active() && s.up.OperatorShare && s.up.OperatorUser != ""
}

// withOperatorAuth appends the operator's mining.authorize to the bytes sent
// to the pool for a customer's login, once per connection. It asks only for a
// device the Gate allowed, on a pool that was marked for it and for a customer
// who gave consent, so that nothing of the operator's is sent otherwise.
func (s *Session) withOperatorAuth(out []byte, a authz) []byte {
	if !s.splitterOn() || a.deviceID == "" || a.customerID == "" {
		return out
	}
	s.mu.Lock()
	asked := s.opAsked
	s.mu.Unlock()
	if asked || !s.srv.Splitter.Eligible(s.up.PoolID, a.deviceID, a.customerID) {
		return out
	}
	req, err := json.Marshal(struct {
		ID     string   `json:"id"`
		Method string   `json:"method"`
		Params []string `json:"params"`
	}{"bitlite-op", "mining.authorize", []string{s.up.OperatorUser, s.up.OperatorPassword}})
	if err != nil {
		return out
	}
	s.mu.Lock()
	s.opAsked = true
	s.mu.Unlock()
	return append(append([]byte(nil), out...), append(req, '\n')...)
}

// operatorRoute decides one share of an allowed device: whether it is credited
// to the operator. The consent, the schedule, the pool's state and the
// switch are all checked on every share; a session that did not get the
// operator's worker accepted never routes anything to the operator.
func (s *Session) operatorRoute(a authz, now time.Time) opshare.Route {
	if !s.splitterOn() {
		return opshare.Route{}
	}
	s.mu.Lock()
	ready := s.opReady
	s.mu.Unlock()
	if !ready {
		return opshare.Route{}
	}
	return s.srv.Splitter.Route(s.up.PoolID, a.deviceID, a.customerID, now)
}

// operatorReady reports whether the pool accepted the operator's worker on
// this connection.
func (s *Session) operatorReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opReady
}

// operatorAuthAnswer handles the pool's answer to the operator's authorize. A
// refusal costs the customer nothing: the session goes on as before, and the
// share is switched off on the pool for a while.
func (s *Session) operatorAuthAnswer(msg *stratum.Message) {
	if stratum.ResultTrue(msg.Result) && stratum.IsNull(msg.Error) {
		s.mu.Lock()
		s.opReady = true
		s.mu.Unlock()
		return
	}
	reason := stratum.ErrorReason(msg.Error)
	if reason == "" {
		reason = "result " + compact(msg.Result)
	}
	if s.srv.Splitter != nil {
		s.srv.Splitter.OperatorRefused(s.up.PoolID, reason)
	}
	s.srv.Events.Throttled("opshare:"+s.up.PoolID, 10*time.Minute, "operator_share_refused",
		"%s did not accept the operator's worker, the share is off on it for a while: %s", s.up.PoolName, reason)
}

// noteShare gives the accounting the pool's answer to one share of a device
// that is tracked for the operator's share.
func (s *Session) noteShare(sub submitInfo, accepted bool, reason string, at time.Time) {
	if !sub.track || s.srv.Splitter == nil {
		return
	}
	s.srv.Splitter.Note(opshare.Outcome{
		DeviceID: sub.deviceID, CustomerID: sub.customerID, PoolID: s.up.PoolID,
		Operator: sub.operator, Window: sub.window, Accepted: accepted, Difficulty: sub.diff, Reason: reason, At: at,
	})
}
