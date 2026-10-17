package session

import (
	"encoding/json"
	"strings"
	"time"
)

// Gate decides which logins may mine. It is the point where device control is
// enforced on the Stratum side: the session asks it for every mining.authorize
// and for every share, and does nothing the Gate refused.
//
// A nil Gate, or one that is not Active, lets everybody in, which is how the
// proxy behaved before device control existed.
type Gate interface {
	// Active reports whether device control is on.
	Active() bool
	// AllowConnect is asked right after a connection's first message, before a
	// pool connection is opened for it: an address that keeps being refused
	// does not get one.
	AllowConnect(ip string) bool
	// Authorize decides one mining.authorize.
	Authorize(req AuthRequest) AuthDecision
	// Usable reports whether a device that was allowed may still mine, and if
	// not, why (a short key).
	Usable(deviceID string) (bool, string)
}

// AuthRequest is what the Gate sees of a mining.authorize.
type AuthRequest struct {
	SessionID uint64
	IP        string
	Transport string
	Login     string
	Password  string // the ASIC's password: the customer's connection key
	UserAgent string
}

// AuthDecision is the Gate's answer.
type AuthDecision struct {
	Allow bool
	// Code is a short key for the outcome ("device_pending", "unauthorized"...),
	// used in logs and alerts. Reason is the text the ASIC gets back.
	Code   string
	Reason string

	CustomerID string
	DeviceID   string
	// Account and Worker fill {account} and {upstream_worker} in the pool's
	// username template.
	Account string
	Worker  string
	// MaxConnections is how many live connections the device may have; the
	// oldest ones are closed when it connects again. Zero means one.
	MaxConnections int
}

// Stratum error code 24 is "unauthorized worker".
const unauthorizedCode = 24

// reply builds a one-line JSON-RPC answer for the ASIC, ending in a newline.
func reply(id json.RawMessage, result any, reason string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	out := struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
		Error  []any           `json:"error"`
	}{id, result, []any{unauthorizedCode, reason, nil}}
	b, _ := json.Marshal(out)
	return append(b, '\n')
}

// deniedError ends a session after the ASIC was told why. It is not a pool
// failure and is not reported as one.
type deniedError struct{ code, reason string }

func (e *deniedError) Error() string { return "refused: " + e.code }

// usesAccount reports whether a pool username template needs the customer's
// own account.
func usesAccount(template string) bool { return strings.Contains(template, "{account}") }

// authz is what a session knows of a login the Gate allowed.
type authz struct {
	deviceID   string
	customerID string
}

// gateActive reports whether the session's server has device control on.
func (s *Session) gateActive() bool {
	return s.srv.Gate != nil && s.srv.Gate.Active()
}

// DeviceIDs returns the devices the session is authorized as.
func (s *Session) DeviceIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.devices))
	for id := range s.devices {
		out = append(out, id)
	}
	return out
}

func (s *Session) hasDevice(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.devices[id]
	return ok
}

func (s *Session) hasCustomer(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.authorized {
		if a.customerID == id {
			return true
		}
	}
	return false
}

// authorizedDevice reports whether any login was allowed through the Gate.
func (s *Session) authorizedDevice() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.devices) > 0
}

// watchAuthorization closes the session if no login was allowed within d.
func (s *Session) watchAuthorization(d time.Duration) (stop func()) {
	if d <= 0 {
		return func() {}
	}
	t := time.AfterFunc(d, func() {
		if !s.authorizedDevice() {
			s.Close("no authorized device within " + d.String())
		}
	})
	return func() { t.Stop() }
}

func truncateLogin(s string) string {
	if r := []rune(s); len(r) > 48 {
		return string(r[:48]) + "…"
	}
	return s
}

// keepOnlyNewest closes the older sessions of a device beyond max (default
// one), so that an ASIC that reconnects replaces its half-dead earlier
// connection instead of being refused for ten minutes, and the same device is
// never counted twice.
func (s *Session) keepOnlyNewest(deviceID string, max int) {
	if deviceID == "" {
		return
	}
	if max <= 0 {
		max = 1
	}
	s.srv.Registry.trimDevice(deviceID, max, s)
}
