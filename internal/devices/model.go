// Package devices keeps the customers of the farm and their devices, and
// decides which of them may mine.
//
// A customer is a row with a short name (the part of an ASIC login before the
// first dot) and a connection key (what the ASIC sends as its password). A
// device is one worker of a customer. The first time an unknown worker shows
// up it is recorded as pending and cannot mine until an administrator approves
// it; an approved device can later be suspended, blocked or revoked, each
// on its own, without touching the customer's other devices.
//
// Everything here is enforced by the server: the Stratum side asks the Gate
// before it forwards an authorize or a share, and a change of state closes the
// live connections of the device at once. The web UI only shows the state.
package devices

import (
	"regexp"
	"strings"
	"time"
)

// DeviceStatus is where a device is in its life.
type DeviceStatus string

const (
	// Pending: seen for the first time, waiting for an administrator.
	Pending DeviceStatus = "pending"
	// Approved: may mine, within the customer's and its own limits.
	Approved DeviceStatus = "approved"
	// Rejected: an administrator said no to a pending device. It can be
	// reopened for another look.
	Rejected DeviceStatus = "rejected"
	// Suspended: paused by an administrator; resume brings it back.
	Suspended DeviceStatus = "suspended"
	// Blocked: cut off and refused on every reconnect; unblock restores the
	// state it was in.
	Blocked DeviceStatus = "blocked"
	// Revoked: access taken away for good. The record stays, so that the same
	// worker name cannot simply register again; deleting the record is the
	// only way back and is an explicit, audited act.
	Revoked DeviceStatus = "revoked"
)

var deviceStatuses = []DeviceStatus{Pending, Approved, Rejected, Suspended, Blocked, Revoked}

func (s DeviceStatus) Valid() bool {
	for _, x := range deviceStatuses {
		if x == s {
			return true
		}
	}
	return false
}

// holdsSlot reports whether a device in this state counts against the
// customer's device limit: the ones that are, or can come back as, working
// devices.
func (s DeviceStatus) holdsSlot() bool { return s == Approved || s == Suspended }

// CustomerStatus is the state of a whole customer.
type CustomerStatus string

const (
	CustomerActive    CustomerStatus = "active"
	CustomerSuspended CustomerStatus = "suspended"
	CustomerBlocked   CustomerStatus = "blocked"
)

func (s CustomerStatus) Valid() bool {
	return s == CustomerActive || s == CustomerSuspended || s == CustomerBlocked
}

// Action is a change of a device's state that an administrator asks for.
type Action string

const (
	ActApprove Action = "approve"
	ActReject  Action = "reject"
	ActReopen  Action = "reopen"
	ActSuspend Action = "suspend"
	ActResume  Action = "resume"
	ActBlock   Action = "block"
	ActUnblock Action = "unblock"
	ActRevoke  Action = "revoke"
)

// Actions in the order the UI shows them.
var Actions = []Action{ActApprove, ActReject, ActReopen, ActSuspend, ActResume, ActBlock, ActUnblock, ActRevoke}

type transition struct {
	from []DeviceStatus
	to   DeviceStatus // "" for unblock: the saved state
}

var transitions = map[Action]transition{
	ActApprove: {[]DeviceStatus{Pending}, Approved},
	ActReject:  {[]DeviceStatus{Pending}, Rejected},
	ActReopen:  {[]DeviceStatus{Rejected}, Pending},
	ActSuspend: {[]DeviceStatus{Approved}, Suspended},
	ActResume:  {[]DeviceStatus{Suspended}, Approved},
	ActBlock:   {[]DeviceStatus{Pending, Approved, Rejected, Suspended}, Blocked},
	ActUnblock: {[]DeviceStatus{Blocked}, ""},
	ActRevoke:  {[]DeviceStatus{Approved, Suspended, Blocked}, Revoked},
}

// Customer is one customer of the farm.
type Customer struct {
	ID   string `json:"id"`
	Slug string `json:"slug"` // the ASIC login is <slug>.<worker>
	Name string `json:"name"`

	Status       CustomerStatus `json:"status"`
	StatusReason string         `json:"status_reason,omitempty"`
	StatusAt     time.Time      `json:"status_at,omitempty"`

	// Account is the customer's own account at the pool (for ViaBTC, the
	// account name). It is public information in a worker name. The pool
	// password is never asked for or stored here.
	Account string `json:"account,omitempty"`

	// MaxDevices caps the devices that are approved or suspended; 0 is no cap.
	MaxDevices int `json:"max_devices"`
	// MaxConnections caps the live connections of the customer; 0 is no cap.
	MaxConnections int `json:"max_connections"`
	// ExpiresAt ends the customer's access; nil never.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`

	Language string `json:"language,omitempty"`
	Notes    string `json:"notes,omitempty"`

	// KeySalt and KeyHash check the connection key; the key itself is shown
	// once, when it is made, and is not kept.
	KeySalt      string    `json:"key_salt"`
	KeyHash      string    `json:"key_hash"`
	KeyRotatedAt time.Time `json:"key_rotated_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Change is one step in the life of a device.
type Change struct {
	At     time.Time    `json:"at"`
	From   DeviceStatus `json:"from"`
	To     DeviceStatus `json:"to"`
	By     string       `json:"by"`
	Reason string       `json:"reason,omitempty"`
}

const maxHistory = 20

// Device is one worker of a customer.
type Device struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	// Worker is the name the ASIC logs in with (the part after the customer's
	// slug). UpstreamWorker is the name made from it for the pool, whose rules
	// are stricter; it is fixed when the device is first seen.
	Worker         string `json:"worker"`
	UpstreamWorker string `json:"upstream_worker"`

	Status       DeviceStatus `json:"status"`
	SavedStatus  DeviceStatus `json:"saved_status,omitempty"` // what unblock returns to
	StatusReason string       `json:"status_reason,omitempty"`
	StatusBy     string       `json:"status_by,omitempty"`
	StatusAt     time.Time    `json:"status_at"`

	// ExpiresAt ends this device's access; nil never.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// MaxConnections is how many live connections the device may have; 0
	// means one, which a reconnecting ASIC replaces.
	MaxConnections int    `json:"max_connections"`
	Notes          string `json:"notes,omitempty"`

	UserAgent string    `json:"user_agent,omitempty"`
	LastIP    string    `json:"last_ip,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Denied counts the logins refused while the device was not approved.
	Denied int `json:"denied,omitempty"`

	History []Change `json:"history,omitempty"`
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{2,31}$`)
	workerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// accountRe is deliberately plain: letters, digits, "_" and "-". It keeps
	// a dot, a space or a quote out of the pool login built from it.
	accountRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
)

// SplitLogin splits an ASIC login into the customer's slug and the worker.
// "acme.rack1.s21-01" is customer "acme", worker "rack1.s21-01"; a login with
// nothing after the dot (or no dot) has the worker "default".
func SplitLogin(login string) (slug, worker string) {
	login = strings.TrimSpace(login)
	slug, worker, found := strings.Cut(login, ".")
	if !found || worker == "" {
		worker = "default"
	}
	return strings.ToLower(slug), worker
}

// ValidSlug reports whether s can be a customer's slug.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// ValidWorker reports whether s can be a worker name.
func ValidWorker(s string) bool { return workerRe.MatchString(s) }

// ValidAccount reports whether s can be a pool account name.
func ValidAccount(s string) bool { return accountRe.MatchString(s) }

// MaxUpstreamWorker is the longest worker name the pools accept (ViaBTC: 64).
const MaxUpstreamWorker = 64

// UpstreamName makes the worker name sent to the pool from the name the ASIC
// uses. Pools differ; the strictest common rule is lowercase Latin letters and
// digits, up to 64 characters (ViaBTC's own rule), so that is what is made:
// "S21-0042" becomes "s210042". Names made from different workers can
// collide; the store adds a number to keep each device's name its own.
func UpstreamName(worker string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(worker) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		s = "w"
	}
	if len(s) > MaxUpstreamWorker {
		s = s[:MaxUpstreamWorker]
	}
	return s
}

// workerKey is how a worker is told from another of the same customer: names
// that differ only in case are one device, so that changing the case of a
// blocked device's name is not a way around the block.
func workerKey(worker string) string { return strings.ToLower(worker) }
