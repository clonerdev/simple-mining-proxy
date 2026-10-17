package devices

import (
	"errors"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
)

// The limiter against guessing connection keys. It counts logins that fail to
// authenticate (an unknown customer or a wrong key) per address, and locks the
// address out of new connections for a while after too many. A login with the
// right key that is refused because the device is pending, blocked or over a
// limit is not a failure here: it is a known customer, and counting it would
// lock out every other customer behind the same NAT address.
const (
	failLimit   = 30
	failWindow  = 10 * time.Minute
	lockFor     = 10 * time.Minute
	maxTracked  = 10000
	maxDedupe   = 5000
	reasonLimit = 128
)

type ipState struct {
	fails       []time.Time
	lockedUntil time.Time
}

// Gate is the session package's Gate backed by the devices store.
type Gate struct {
	st      *Store
	enabled func() bool
	pending func() int
	live    func(customerID, exceptDeviceID string) int
	now     func() time.Time

	mu    sync.Mutex
	ips   map[string]*ipState
	dedup map[string]time.Time
}

// GateConfig wires a Gate. Enabled and PendingLimit read the current
// settings; Live counts the live sessions of a customer, not counting the ones
// of one device.
type GateConfig struct {
	Store        *Store
	Enabled      func() bool
	PendingLimit func() int
	Live         func(customerID, exceptDeviceID string) int
}

var _ session.Gate = (*Gate)(nil)

// NewGate makes the Gate.
func NewGate(c GateConfig) *Gate {
	g := &Gate{st: c.Store, enabled: c.Enabled, pending: c.PendingLimit, live: c.Live, now: time.Now,
		ips: map[string]*ipState{}, dedup: map[string]time.Time{}}
	if g.enabled == nil {
		g.enabled = func() bool { return true }
	}
	if g.pending == nil {
		g.pending = func() int { return 10 }
	}
	if g.live == nil {
		g.live = func(string, string) int { return 0 }
	}
	return g
}

// Active reports whether device control is on.
func (g *Gate) Active() bool { return g.enabled() }

// Usable reports whether the device may still mine.
func (g *Gate) Usable(deviceID string) (bool, string) { return g.st.Usable(deviceID) }

// AllowConnect refuses an address that is locked for guessing keys.
func (g *Gate) AllowConnect(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.ips[ip]
	return st == nil || !g.now().Before(st.lockedUntil)
}

func (g *Gate) recordFail(ip string, now time.Time) (locked bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.ips[ip]
	if st == nil {
		if len(g.ips) >= maxTracked {
			for k, v := range g.ips { // forget the quiet ones
				if len(v.fails) == 0 || now.Sub(v.fails[len(v.fails)-1]) > failWindow {
					delete(g.ips, k)
				}
			}
			if len(g.ips) >= maxTracked {
				return false
			}
		}
		st = &ipState{}
		g.ips[ip] = st
	}
	kept := st.fails[:0]
	for _, t := range st.fails {
		if now.Sub(t) < failWindow {
			kept = append(kept, t)
		}
	}
	st.fails = append(kept, now)
	if len(st.fails) >= failLimit {
		st.lockedUntil = now.Add(lockFor)
		st.fails = nil
		return true
	}
	return false
}

func (g *Gate) isLocked(ip string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.ips[ip]
	return st != nil && now.Before(st.lockedUntil)
}

// once reports true the first time key is seen in the window: the alerts about
// a repeated problem are sent once, not once per retry.
func (g *Gate) once(key string, now time.Time, window time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if t, ok := g.dedup[key]; ok && now.Sub(t) < window {
		return false
	}
	if len(g.dedup) >= maxDedupe {
		for k, t := range g.dedup {
			if now.Sub(t) >= window {
				delete(g.dedup, k)
			}
		}
		if len(g.dedup) >= maxDedupe {
			return false
		}
	}
	g.dedup[key] = now
	return true
}

// The texts an ASIC shows for a refusal. They are short, English and say
// nothing about which part of an unknown login was wrong.
var refusals = map[string]string{
	"unauthorized":       "Unauthorized worker",
	"throttled":          "Too many failed attempts, try again later",
	"worker_invalid":     "Invalid worker name",
	"pending_limit":      "Too many devices are waiting for approval",
	"device_pending":     "Device is waiting for approval",
	"device_rejected":    "Device was rejected",
	"device_suspended":   "Device is suspended",
	"device_blocked":     "Device is blocked",
	"device_revoked":     "Device access was revoked",
	"device_expired":     "Device access has expired",
	"customer_suspended": "Account is suspended",
	"customer_blocked":   "Account is blocked",
	"customer_expired":   "Account access has expired",
	"connection_limit":   "Connection limit reached",
	"internal":           "Temporary error, try again",
}

func refuse(code string) session.AuthDecision {
	r, ok := refusals[code]
	if !ok {
		r = refusals["unauthorized"]
	}
	return session.AuthDecision{Code: code, Reason: r}
}

// Authorize decides a mining.authorize.
func (g *Gate) Authorize(req session.AuthRequest) session.AuthDecision {
	now := g.now()
	if g.isLocked(req.IP, now) {
		return refuse("throttled")
	}
	slug, worker := SplitLogin(req.Login)
	c, known := g.st.CustomerBySlug(slug)
	// The key is checked against a dummy for an unknown customer, so the answer
	// takes the same time and does not tell which part was wrong.
	keyOK := CheckKey(map[bool]Customer{true: c, false: dummyCustomer}[known], req.Password)
	if !known || !keyOK {
		if g.recordFail(req.IP, now) && g.once("lock:"+req.IP, now, suspiciousThrottle) {
			g.st.emit(Event{Type: EvDeviceSuspicious, At: now, Actor: audit.Actor{Type: audit.ActorAnonymous, IP: req.IP}, IP: req.IP,
				Reason: "ip_locked", Details: map[string]any{"locked_for": lockFor.String()}})
		}
		if known && g.once("key:"+req.IP+":"+c.ID, now, suspiciousThrottle) {
			g.st.emit(Event{Type: EvDeviceSuspicious, At: now, Actor: audit.Actor{Type: audit.ActorAnonymous, IP: req.IP},
				CustomerID: c.ID, CustomerSlug: c.Slug, CustomerName: c.Name, Worker: worker, IP: req.IP, Reason: "wrong_key"})
		}
		return refuse("unauthorized")
	}
	if !ValidWorker(worker) {
		return refuse("worker_invalid")
	}
	// A customer that is suspended, blocked or expired gets no new devices made
	// for it either.
	if c.Status != CustomerActive {
		return refuse("customer_" + string(c.Status))
	}
	if c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) {
		return refuse("customer_expired")
	}

	dev, _, err := g.st.Register(c, worker, req.UserAgent, req.IP, g.pending())
	switch {
	case errors.Is(err, ErrPendingLimit):
		if g.once("pending:"+c.ID, now, suspiciousThrottle) {
			g.st.emit(Event{Type: EvDeviceSuspicious, At: now, Actor: audit.Actor{Type: audit.ActorDevice, Name: slug + "." + worker, IP: req.IP},
				CustomerID: c.ID, CustomerSlug: c.Slug, CustomerName: c.Name, Worker: worker, IP: req.IP, Reason: "pending_limit"})
		}
		return refuse("pending_limit")
	case err != nil:
		return refuse("internal")
	}

	if ok, why := Usable(c, dev, now); !ok {
		g.st.NoteSeen(dev.ID, req.UserAgent, req.IP, true)
		return decision(refuse(why), c, dev)
	}
	if c.MaxConnections > 0 && g.live(c.ID, dev.ID) >= c.MaxConnections {
		if g.once("limit:"+dev.ID, now, suspiciousThrottle) {
			g.st.emit(Event{Type: EvDeviceSuspicious, At: now, Actor: audit.Actor{Type: audit.ActorDevice, Name: slug + "." + worker, IP: req.IP},
				CustomerID: c.ID, CustomerSlug: c.Slug, CustomerName: c.Name, DeviceID: dev.ID, Worker: dev.Worker, IP: req.IP, Reason: "connection_limit"})
		}
		g.st.NoteSeen(dev.ID, req.UserAgent, req.IP, true)
		return decision(refuse("connection_limit"), c, dev)
	}
	g.st.NoteSeen(dev.ID, req.UserAgent, req.IP, false)
	d := decision(session.AuthDecision{Allow: true, Code: "approved"}, c, dev)
	d.Account, d.Worker, d.MaxConnections = c.Account, dev.UpstreamWorker, dev.MaxConnections
	return d
}

func decision(d session.AuthDecision, c Customer, dev Device) session.AuthDecision {
	d.CustomerID, d.DeviceID = c.ID, dev.ID
	return d
}
