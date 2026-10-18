package devices

import (
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
)

// Event types. The same strings are the audit actions.
const (
	EvCustomerCreated  = "customer.create"
	EvCustomerUpdated  = "customer.update"
	EvCustomerStatus   = "customer.status"
	EvCustomerKey      = "customer.key_rotate"
	EvCustomerConsent  = "customer.fee_consent" // To is "granted" or "revoked"
	EvCustomerDeleted  = "customer.delete"
	EvDevicePending    = "device.pending"
	EvDeviceUpdated    = "device.update"
	EvDeviceDeleted    = "device.delete"
	EvDeviceSuspicious = "device.suspicious" // wrong key, flood, limit hit
	EvDeviceExpired    = "device.expired"
	EvDeviceApproved   = "device.approve"
	EvDeviceRejected   = "device.reject"
	EvDeviceReopened   = "device.reopen"
	EvDeviceSuspended  = "device.suspend"
	EvDeviceResumed    = "device.resume"
	EvDeviceBlocked    = "device.block"
	EvDeviceUnblocked  = "device.unblock"
	EvDeviceRevoked    = "device.revoke"
	EvCustomerCutOff   = "customer.cut_off" // status or expiry took the customer's devices offline
	eventHandlersMax   = 16
	suspiciousThrottle = 10 * time.Minute
)

// Event is something that happened to a customer or a device. It is sent to
// the handlers registered with OnEvent after the change is on disk: they close
// live connections, write the audit log and send the alerts.
type Event struct {
	Type  string
	At    time.Time
	Actor audit.Actor

	CustomerID   string
	CustomerSlug string
	CustomerName string
	DeviceID     string
	Worker       string

	From, To string // states, for a change of state
	Reason   string
	IP       string
	Details  map[string]any
}

// Target is the audit target of the event.
func (e Event) Target() audit.Target {
	if e.DeviceID != "" {
		return audit.Target{Type: "device", ID: e.DeviceID, Name: e.CustomerSlug + "." + e.Worker}
	}
	return audit.Target{Type: "customer", ID: e.CustomerID, Name: e.CustomerSlug}
}

// OnEvent registers fn to be called, in order, for every event. fn runs on
// the goroutine that made the change, outside the store's locks; it must be
// quick and must not block.
func (s *Store) OnEvent(fn func(Event)) {
	s.hmu.Lock()
	defer s.hmu.Unlock()
	if len(s.handlers) < eventHandlersMax {
		s.handlers = append(s.handlers, fn)
	}
}

func (s *Store) emit(e Event) {
	if e.At.IsZero() {
		e.At = s.now().UTC()
	}
	s.hmu.Lock()
	hs := append([]func(Event){}, s.handlers...)
	s.hmu.Unlock()
	for _, h := range hs {
		h(e)
	}
}
