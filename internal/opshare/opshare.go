// Package opshare is the operator's optional share of a customer's mining.
//
// What it does, and what it does not. A share is credited to the account
// that the pool sees in the worker name of the submit message. During short,
// fixed windows of every cycle the proxy puts the operator's worker in that
// place instead of the customer's. The ASIC never stops and never reconnects.
// Nothing here moves money: the pool pays whoever the share was credited to.
// The package records how much work went where, so that the operator can show
// the customer an account of it.
//
// It only works with a pool that accepts a second, operator, worker on the
// connection the customer's ASIC opened, and credits shares to it. The pool's
// documentation says nothing either way for ViaBTC, so it is off by default,
// is switched on per pool, and a pool that refuses the operator's worker or
// rejects its shares switches itself off (see Router.Note).
//
// Three conditions must hold for a share to be routed to the operator, all
// checked on the server for every share: the proxy's switch is on, the pool
// allows it, and the customer has given their consent. Without consent a
// customer's shares always go to the customer.
package opshare

import (
	"hash/fnv"
	"time"
)

// Subject is what a rule is asked about.
type Subject struct {
	CustomerID string
	DeviceID   string
	PlanID     string
}

// Decision is the share a rule set gives the operator for a subject.
type Decision struct {
	// Fraction of the mining time, 0 to 1. Zero means no share. 2 % is 0.02;
	// one hour a day is 1/24.
	Fraction float64
	// RuleID is the rule the share comes from, for the accounting.
	RuleID string
}

// Rules says how big the operator's share of a subject is. The billing engine
// implements it; the share is a rule like the other revenue rules, with a
// scope, a priority and a validity.
type Rules interface {
	Share(sub Subject, now time.Time) Decision
}

// Consents tells whether a customer agreed to the share. It is checked on
// every share, so a withdrawn consent stops the share at once.
type Consents interface {
	Consented(customerID string) bool
}

// Config is the proxy-wide configuration, read live so that a settings change
// applies without a restart.
type Config struct {
	Enabled bool
	// Cycle is the length of one scheduling cycle; the operator's window of a
	// device sits once in every cycle.
	Cycle time.Duration
	// MaxFraction caps whatever the rules say: a typo in a rule cannot take
	// half of a customer's mining. Zero is the default cap.
	MaxFraction float64
}

// DefaultCycle and DefaultMaxFraction are the values used when the Config does
// not set them.
const (
	DefaultCycle       = 15 * time.Minute
	DefaultMaxFraction = 0.10
)

func (c Config) cycle() time.Duration {
	if c.Cycle < time.Minute {
		return DefaultCycle
	}
	return c.Cycle
}

func (c Config) max() float64 {
	if c.MaxFraction <= 0 || c.MaxFraction > 1 {
		return DefaultMaxFraction
	}
	return c.MaxFraction
}

// Window is where a moment lies in the operator's schedule of one device.
type Window struct {
	// In is true when the moment is inside the operator's window.
	In bool
	// Start identifies the window: the Unix time its cycle began. Windows are
	// counted by it, so a restart or a second connection of the same device
	// never counts the same window twice.
	Start int64
	// Length of the window.
	Length time.Duration
}

// WindowAt places the window of device in the cycle containing t. The schedule
// is a pure function of the clock, the device and the fraction: nothing is
// remembered, so a restart or a reconnect picks it up where it was, and every
// connection of the device agrees. The offset inside the cycle comes from a
// hash of the device id, so the devices of a farm do not all switch together.
func WindowAt(deviceID string, fraction float64, cycle time.Duration, t time.Time) Window {
	if fraction <= 0 || cycle <= 0 {
		return Window{}
	}
	if fraction > 1 {
		fraction = 1
	}
	secs := int64(cycle / time.Second)
	if secs <= 0 {
		return Window{}
	}
	unix := t.Unix()
	start := unix - ((unix%secs)+secs)%secs
	length := time.Duration(fraction * float64(cycle))
	w := Window{Start: start, Length: length}
	if length <= 0 {
		return w
	}
	free := cycle - length
	var off time.Duration
	if free > 0 {
		h := fnv.New64a()
		h.Write([]byte(deviceID))
		off = time.Duration(h.Sum64() % uint64(free))
	}
	at := t.Sub(time.Unix(start, 0))
	w.In = at >= off && at < off+length
	return w
}

// Outcome is the pool's answer to one share, for the accounting.
type Outcome struct {
	DeviceID   string
	CustomerID string
	PoolID     string
	// Operator is true when the share was credited to the operator's worker.
	Operator bool
	// Window is the Start of the operator's window the share fell in; only
	// meaningful when Operator is true.
	Window     int64
	Accepted   bool
	Difficulty float64
	Reason     string
	At         time.Time
}

// Splitter is what a Stratum session asks. A nil Splitter, or one that is not
// Active, never routes anything to the operator.
type Splitter interface {
	// Active reports whether the proxy-wide switch is on.
	Active() bool
	// Eligible reports whether shares of the device may be credited to the
	// operator on this pool now. A session asks once, when a login is
	// authorized, to decide whether to authorize the operator's worker too.
	Eligible(poolID, deviceID, customerID string) bool
	// Route decides one share. It checks consent and the schedule on every
	// call.
	Route(poolID, deviceID, customerID string, now time.Time) Route
	// Note records the pool's answer to a share, after it came.
	Note(o Outcome)
	// OperatorRefused is called when the pool did not accept the operator's
	// worker on the connection; the share is not used on that pool for a while.
	OperatorRefused(poolID, reason string)
}

// Route is the decision for one share.
type Route struct {
	Operator bool
	Window   int64
}

// DayUsage is the account of one device for one UTC day.
type DayUsage struct {
	Day        string `json:"day"` // 2006-01-02
	CustomerID string `json:"customer_id"`
	DeviceID   string `json:"device_id"`
	RuleID     string `json:"rule_id,omitempty"`

	// Windows is how many windows of the operator carried at least one share,
	// and AllocatedSeconds their total length. A window counts when a share
	// was credited to the operator in it; a window in which the device sent
	// nothing is not counted.
	Windows          int   `json:"windows"`
	AllocatedSeconds int64 `json:"allocated_seconds"`

	OperatorAccepted   uint64  `json:"operator_accepted"`
	OperatorRejected   uint64  `json:"operator_rejected"`
	OperatorDifficulty float64 `json:"operator_difficulty"` // sum over accepted shares
	CustomerAccepted   uint64  `json:"customer_accepted"`
	CustomerRejected   uint64  `json:"customer_rejected"`
	CustomerDifficulty float64 `json:"customer_difficulty"`
}

// UsageReader gives the accounting of a period.
type UsageReader interface {
	// Usage returns one row per device and day with activity from from to to
	// (UTC days, both included), for one customer, or for all when customerID
	// is empty. It includes what has not been written to disk yet.
	Usage(from, to time.Time, customerID string) []DayUsage
}

// Share of work credited to the operator, from a usage row; zero without any.
func (u DayUsage) OperatorShareOfWork() float64 {
	total := u.OperatorDifficulty + u.CustomerDifficulty
	if total <= 0 {
		return 0
	}
	return u.OperatorDifficulty / total
}
