// Package accounts keeps the admin panel's users: who they are, which role
// they have, which permissions follow from it, and a password hash for each.
// The administrator set by ADMIN_USERNAME and ADMIN_PASSWORD is not stored
// here: it always exists, always has every permission and is the way back in
// when the accounts file is lost, so a simple single-administrator install
// needs nothing from this package.
package accounts

import "sort"

// Role is a named set of default permissions.
type Role string

const (
	// RoleSuperAdmin has every permission and cannot be limited.
	RoleSuperAdmin Role = "superadmin"
	// RoleOperator watches the farm and handles devices; it cannot change
	// settings or pools, manage users or touch money rules unless granted.
	RoleOperator Role = "operator"
)

// Roles lists the roles in the order the UI shows them.
var Roles = []Role{RoleSuperAdmin, RoleOperator}

func (r Role) Valid() bool {
	for _, x := range Roles {
		if x == r {
			return true
		}
	}
	return false
}

// Permission is one thing an account may do. Permissions are checked by the
// server on every API call; hiding a button in the UI is never the check.
type Permission string

const (
	StatsView     Permission = "stats.view"     // dashboard totals and charts
	MinersView    Permission = "miners.view"    // the list of connected miners
	MinersControl Permission = "miners.control" // reconnect all miners
	EventsView    Permission = "events.view"    // the event log
	SettingsView  Permission = "settings.view"  // pools and settings, read only
	SettingsEdit  Permission = "settings.edit"  // change pools, settings, bot
	UsersManage   Permission = "users.manage"   // accounts, roles, passwords
	DevicesView   Permission = "devices.view"   // customers and devices
	DevicesManage Permission = "devices.manage" // approve, block, limits
	BillingView   Permission = "billing.view"   // reports and the ledger
	BillingManage Permission = "billing.manage" // revenue rules, settlement
	AuditView     Permission = "audit.view"     // the audit log
)

// StaffPermissions are every permission of the administration. Customers have
// no account and no access to the panel: everything about them is done by the
// administration.
var StaffPermissions = []Permission{
	StatsView, MinersView, MinersControl, EventsView, SettingsView, SettingsEdit,
	UsersManage, DevicesView, DevicesManage, BillingView, BillingManage, AuditView,
}

var rolePermissions = map[Role][]Permission{
	RoleSuperAdmin: append([]Permission{}, StaffPermissions...),
	RoleOperator:   {StatsView, MinersView, EventsView, SettingsView, DevicesView, DevicesManage, BillingView},
}

// RolePermissions returns the default permissions of a role.
func RolePermissions(r Role) []Permission {
	return append([]Permission(nil), rolePermissions[r]...)
}

// Valid reports whether p is a known permission.
func (p Permission) Valid() bool {
	for _, x := range StaffPermissions {
		if x == p {
			return true
		}
	}
	return false
}

// Set is a set of permissions.
type Set map[Permission]struct{}

func (s Set) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}

// List returns the permissions sorted by name.
func (s Set) List() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Effective is what an account may do: the permissions of its role, plus the
// ones granted to it, minus the ones revoked. A super administrator always has
// everything, so that no combination of edits can lock the administration out.
// An account with a role that is not known (one stored by an older version,
// such as the customer role that no longer exists) has nothing, whatever was
// granted to it.
func Effective(role Role, grant, revoke []Permission) Set {
	out := Set{}
	if !role.Valid() {
		return out
	}
	for _, p := range rolePermissions[role] {
		out[p] = struct{}{}
	}
	if role == RoleSuperAdmin {
		return out
	}
	for _, p := range grant {
		if !p.Valid() {
			continue
		}
		out[p] = struct{}{}
	}
	for _, p := range revoke {
		delete(out, p)
	}
	return out
}

// All is the set of an administrator: the environment administrator and the
// API token have it.
func All() Set { return Effective(RoleSuperAdmin, nil, nil) }
