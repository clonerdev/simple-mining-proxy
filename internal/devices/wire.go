package devices

import (
	"context"
	"fmt"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
)

// DefaultEnforceEvery is how often Enforce looks for connections whose device
// may no longer mine.
const DefaultEnforceEvery = 10 * time.Second

// Enforcement connects the devices store to the live Stratum sessions, the
// audit log and the event list. It is what makes a change of state real: the
// store records it, and the handler installed here closes the connections of
// the device (or customer) at once, writes the audit entry and tells the
// alert channels.
//
// Nothing here is optional for security. Without it a blocked device would
// only be refused on its next share or login.
type Enforcement struct {
	Store    *Store
	Registry *session.Registry
	Audit    *audit.Log
	Events   *events.Log
	// Notify receives every event after the connections were cut and the
	// audit entry was written, for alerts. It must not block. May be nil.
	Notify func(Event)
	// Every is the period of the check for expired devices; zero is
	// DefaultEnforceEvery.
	Every time.Duration

	now func() time.Time
}

// Install registers the event handler on the store. Call it once, before the
// Stratum listeners start.
func (e *Enforcement) Install() {
	if e.now == nil {
		e.now = time.Now
	}
	e.Store.OnEvent(e.handle)
}

// Run checks every Every whether a live connection belongs to a device that
// is no longer allowed to mine (it expired, or its customer did), and closes
// it. A device that sends shares is also checked on every share; this covers
// the one that sits silent. Returns when ctx ends.
func (e *Enforcement) Run(ctx context.Context) {
	every := e.Every
	if every <= 0 {
		every = DefaultEnforceEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Sweep()
		}
	}
}

// Sweep closes the live connections of devices that may not mine now and
// returns how many connections it closed. A device found expired is reported
// once, as an event.
func (e *Enforcement) Sweep() int {
	closed := 0
	seen := map[string]bool{}
	for _, s := range e.Registry.List() {
		for _, id := range s.DeviceIDs() {
			if seen[id] {
				continue
			}
			seen[id] = true
			ok, why := e.Store.Usable(id)
			if ok {
				continue
			}
			n := e.Registry.CloseDevice(id, "device may no longer mine: "+why)
			closed += n
			if n == 0 {
				continue
			}
			d, found := e.Store.Device(id)
			if !found {
				continue
			}
			c, _ := e.Store.Customer(d.CustomerID)
			if why == "device_expired" || why == "customer_expired" {
				e.Store.emit(Event{Type: EvDeviceExpired, Actor: audit.System, CustomerID: c.ID, CustomerSlug: c.Slug,
					CustomerName: c.Name, DeviceID: d.ID, Worker: d.Worker, Reason: why})
			}
		}
	}
	return closed
}

func (e *Enforcement) handle(ev Event) {
	cut := e.cut(ev)
	e.audit(ev, cut)
	e.log(ev, cut)
	if e.Notify != nil {
		e.Notify(ev)
	}
}

// cut closes the connections an event makes illegal and returns how many.
func (e *Enforcement) cut(ev Event) int {
	if e.Registry == nil {
		return 0
	}
	switch ev.Type {
	case EvDeviceSuspended, EvDeviceBlocked, EvDeviceRevoked, EvDeviceRejected, EvDeviceDeleted, EvDeviceExpired:
		if ev.DeviceID == "" {
			return 0
		}
		return e.Registry.CloseDevice(ev.DeviceID, "device "+ev.Type[len("device."):])
	case EvCustomerStatus:
		if ev.To != string(CustomerActive) {
			return e.Registry.CloseCustomer(ev.CustomerID, "customer "+ev.To)
		}
	case EvCustomerDeleted:
		return e.Registry.CloseCustomer(ev.CustomerID, "customer deleted")
	case EvCustomerKey:
		// A new connection key makes every login with the old one invalid, but
		// a live connection already passed the check. They stay: closing them
		// would stop mining for a routine key change, and the next login must
		// use the new key anyway.
		return 0
	case EvDeviceUpdated, EvCustomerUpdated:
		// A shorter expiry or a lower limit can make a live connection
		// illegal at once.
		return e.Sweep()
	}
	return 0
}

func (e *Enforcement) audit(ev Event, cut int) {
	details := map[string]any{}
	for k, v := range ev.Details {
		details[k] = v
	}
	if ev.From != "" || ev.To != "" {
		details["from"], details["to"] = ev.From, ev.To
	}
	if ev.IP != "" {
		details["ip"] = ev.IP
	}
	if cut > 0 {
		details["connections_closed"] = cut
	}
	result := audit.OK
	if ev.Type == EvDeviceSuspicious {
		result = audit.Denied
	}
	e.Audit.Add(audit.Entry{Time: ev.At, Actor: ev.Actor, Action: ev.Type, Target: ev.Target(),
		Result: result, Message: ev.Reason, Details: details})
}

func (e *Enforcement) log(ev Event, cut int) {
	if e.Events == nil {
		return
	}
	who := ev.CustomerSlug
	if ev.Worker != "" {
		who += "." + ev.Worker
	}
	switch ev.Type {
	case EvDevicePending:
		e.Events.Warn("device_pending", "new device %s is waiting for approval (from %s)", who, ev.IP)
	case EvDeviceSuspicious:
		e.Events.Throttled("suspicious:"+ev.Reason+":"+ev.IP+":"+who, 10*time.Minute, "device_suspicious",
			"suspicious login %s from %s: %s", who, ev.IP, ev.Reason)
	case EvDeviceExpired:
		e.Events.Warn("device_expired", "device %s expired (%s)%s", who, ev.Reason, closedNote(cut))
	case EvDeviceApproved, EvDeviceRejected, EvDeviceReopened, EvDeviceSuspended, EvDeviceResumed,
		EvDeviceBlocked, EvDeviceUnblocked, EvDeviceRevoked:
		e.Events.Info("device_state", "device %s: %s → %s (%s)%s", who, ev.From, ev.To, ev.Type, closedNote(cut))
	case EvCustomerStatus:
		e.Events.Info("customer_state", "customer %s: %s → %s%s", ev.CustomerSlug, ev.From, ev.To, closedNote(cut))
	case EvCustomerCreated, EvCustomerDeleted, EvCustomerKey, EvDeviceDeleted:
		e.Events.Info("customer_change", "%s: %s", ev.Type, who)
	}
}

func closedNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("; %d connection(s) closed", n)
}
