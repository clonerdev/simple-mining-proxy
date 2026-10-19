package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/accounts"
	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/export"
)

var errNoAccounts = apierr.Conflict(apierr.M("accounts_unavailable", "accounts are not available on this server"))

// userView is an account as the API shows it: never the password hash.
type userView struct {
	ID          string                `json:"id"`
	Username    string                `json:"username"`
	DisplayName string                `json:"display_name"`
	Role        accounts.Role         `json:"role"`
	Grant       []accounts.Permission `json:"grant"`
	Revoke      []accounts.Permission `json:"revoke"`
	Permissions []accounts.Permission `json:"permissions"`
	Language    string                `json:"language"`
	Disabled    bool                  `json:"disabled"`
	CreatedAt   time.Time             `json:"created_at"`
	UpdatedAt   time.Time             `json:"updated_at"`
	LastLoginAt *time.Time            `json:"last_login_at"`
}

func viewOf(u accounts.User) userView {
	v := userView{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role,
		Grant: nonNil(u.Grant), Revoke: nonNil(u.Revoke), Permissions: u.Permissions().List(),
		Language: u.Language, Disabled: u.Disabled,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
	if !u.LastLoginAt.IsZero() {
		t := u.LastLoginAt
		v.LastLoginAt = &t
	}
	return v
}

func nonNil(p []accounts.Permission) []accounts.Permission {
	if p == nil {
		return []accounts.Permission{}
	}
	return p
}

// accountsErr turns an accounts error into an API error.
func accountsErr(err error) error {
	var e *accounts.Error
	switch {
	case errors.As(err, &e):
		fields := map[string]apierr.Msg{}
		// A literal, not apierr.M: the text comes from the accounts package and
		// the key is "account_" plus the problem, translated by the web UI.
		text := accounts.Messages[e.Key]
		for k, v := range e.Params {
			text = strings.ReplaceAll(text, "{"+k+"}", fmt.Sprint(v))
		}
		msg := apierr.Msg{Key: "account_" + e.Key, Params: e.Params, Text: text}
		if e.Field != "" {
			fields[e.Field] = msg
		}
		return apierr.Validation(apierr.M("account_invalid", "the account data is invalid"), fields)
	case errors.Is(err, accounts.ErrNotFound):
		return apierr.NotFound(apierr.M("account_not_found", "account not found"))
	}
	return err
}

// mayAssign is the rule against privilege escalation: an account can only
// hand out what it has itself. The super administrator role is held by super
// administrators (and the environment administrator and API token) alone.
func mayAssign(who *principal, role accounts.Role, grant []accounts.Permission) error {
	if role == accounts.RoleSuperAdmin && who.Role != accounts.RoleSuperAdmin {
		return errEscalation
	}
	for _, p := range grant {
		if !who.perms.Has(p) {
			return errEscalation
		}
	}
	return nil
}

var errEscalation = apierr.Forbidden(apierr.M("escalation",
	"you cannot give an account more access than you have"))

func (s *Server) roles(w http.ResponseWriter, r *http.Request) error {
	type roleView struct {
		Role        accounts.Role         `json:"role"`
		Permissions []accounts.Permission `json:"permissions"`
	}
	var rs []roleView
	for _, role := range accounts.Roles {
		rs = append(rs, roleView{role, accounts.RolePermissions(role)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"roles":       rs,
		"permissions": accounts.StaffPermissions,
		"languages":   accounts.Languages,
	})
	return nil
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) error {
	out := []userView{}
	if s.d.Accounts != nil {
		for _, u := range s.d.Accounts.List() {
			out = append(out, viewOf(u))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "env_admin": s.d.Config.AdminUsername})
	return nil
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) error {
	if s.d.Accounts == nil {
		return errNoAccounts
	}
	var in struct {
		Username    string                `json:"username"`
		DisplayName string                `json:"display_name"`
		Role        accounts.Role         `json:"role"`
		Grant       []accounts.Permission `json:"grant"`
		Revoke      []accounts.Permission `json:"revoke"`
		Language    string                `json:"language"`
		Password    string                `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	who := principalOf(r)
	if err := mayAssign(who, in.Role, in.Grant); err != nil {
		s.audit(r, "user.create", audit.Target{Type: "user", Name: in.Username}, map[string]any{"role": string(in.Role)})
		return err
	}
	u, err := s.d.Accounts.Create(accounts.NewUser{
		Username: in.Username, DisplayName: in.DisplayName, Role: in.Role, Grant: in.Grant, Revoke: in.Revoke,
		Language: in.Language, Password: in.Password,
	})
	if err != nil {
		return accountsErr(err)
	}
	s.audit(r, "user.create", audit.Target{Type: "user", ID: u.ID, Name: u.Username},
		map[string]any{"role": string(u.Role), "grant": u.Grant, "revoke": u.Revoke})
	writeJSON(w, http.StatusCreated, viewOf(u))
	return nil
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) error {
	if s.d.Accounts == nil {
		return errNoAccounts
	}
	id := r.PathValue("id")
	cur, ok := s.d.Accounts.Get(id)
	if !ok {
		return accountsErr(accounts.ErrNotFound)
	}
	var in struct {
		DisplayName *string                `json:"display_name"`
		Role        *accounts.Role         `json:"role"`
		Grant       *[]accounts.Permission `json:"grant"`
		Revoke      *[]accounts.Permission `json:"revoke"`
		Language    *string                `json:"language"`
		Disabled    *bool                  `json:"disabled"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	who := principalOf(r)
	self := who.UserID == id
	// Nobody edits their own access, or switches themselves off: that is how
	// an administration locks itself out or promotes itself.
	if self && (in.Role != nil || in.Grant != nil || in.Revoke != nil || in.Disabled != nil) {
		return apierr.Conflict(apierr.M("account_self_edit", "you cannot change your own role, permissions or status"))
	}
	if cur.Role == accounts.RoleSuperAdmin && who.Role != accounts.RoleSuperAdmin {
		return errEscalation
	}
	newRole := cur.Role
	if in.Role != nil {
		newRole = *in.Role
	}
	var grant []accounts.Permission
	if in.Grant != nil {
		grant = *in.Grant
	}
	if err := mayAssign(who, newRole, grant); err != nil {
		return err
	}
	u, err := s.d.Accounts.Update(id, accounts.Update{
		DisplayName: in.DisplayName, Role: in.Role, Grant: in.Grant, Revoke: in.Revoke,
		Language: in.Language, Disabled: in.Disabled,
	})
	if err != nil {
		return accountsErr(err)
	}
	if u.Disabled {
		s.auth.revoke(id)
	}
	action := "user.update"
	switch {
	case in.Disabled != nil && *in.Disabled:
		action = "user.disable"
	case in.Disabled != nil && !*in.Disabled:
		action = "user.enable"
	}
	s.audit(r, action, audit.Target{Type: "user", ID: u.ID, Name: u.Username},
		map[string]any{"role": string(u.Role), "grant": u.Grant, "revoke": u.Revoke, "disabled": u.Disabled})
	writeJSON(w, http.StatusOK, viewOf(u))
	return nil
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) error {
	if s.d.Accounts == nil {
		return errNoAccounts
	}
	id := r.PathValue("id")
	cur, ok := s.d.Accounts.Get(id)
	if !ok {
		return accountsErr(accounts.ErrNotFound)
	}
	who := principalOf(r)
	if who.UserID == id {
		return apierr.Conflict(apierr.M("account_self_delete", "you cannot delete your own account"))
	}
	if cur.Role == accounts.RoleSuperAdmin && who.Role != accounts.RoleSuperAdmin {
		return errEscalation
	}
	if err := s.d.Accounts.Delete(id); err != nil {
		return accountsErr(err)
	}
	s.auth.revoke(id)
	s.audit(r, "user.delete", audit.Target{Type: "user", ID: id, Name: cur.Username}, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

func (s *Server) resetUserPassword(w http.ResponseWriter, r *http.Request) error {
	if s.d.Accounts == nil {
		return errNoAccounts
	}
	id := r.PathValue("id")
	cur, ok := s.d.Accounts.Get(id)
	if !ok {
		return accountsErr(accounts.ErrNotFound)
	}
	who := principalOf(r)
	if cur.Role == accounts.RoleSuperAdmin && who.Role != accounts.RoleSuperAdmin {
		return errEscalation
	}
	var in struct {
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := s.d.Accounts.SetPassword(id, in.Password); err != nil {
		return accountsErr(err)
	}
	s.auth.revoke(id)
	s.audit(r, "user.password_reset", audit.Target{Type: "user", ID: id, Name: cur.Username}, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// changeOwnPassword lets an account change its own password. The sessions of
// the account end, this one included, and a fresh one is opened.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) error {
	who := principalOf(r)
	if who.UserID == "" || s.d.Accounts == nil {
		return apierr.Conflict(apierr.M("password_env", "this administrator's password is set by ADMIN_PASSWORD in the environment"))
	}
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := decode(r, &in); err != nil {
		return err
	}
	s.auth.hashing <- struct{}{}
	_, ok := s.d.Accounts.Authenticate(who.Username, in.Current)
	<-s.auth.hashing
	if !ok {
		s.audit(r, "user.password_change", audit.Target{Type: "user", ID: who.UserID, Name: who.Username}, nil)
		return apierr.Validation(apierr.M("account_invalid", "the account data is invalid"),
			map[string]apierr.Msg{"current_password": apierr.M("account_password_current", "the current password is wrong")})
	}
	if err := s.d.Accounts.SetPassword(who.UserID, in.New); err != nil {
		return accountsErr(err)
	}
	s.auth.revoke(who.UserID)
	id := s.auth.open(who.UserID, time.Now())
	s.auth.setCookie(w, r, id, int(sessionLifetime/time.Second))
	s.audit(r, "user.password_change", audit.Target{Type: "user", ID: who.UserID, Name: who.Username}, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	return nil
}

// ---- audit log ----

func parseWhen(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, true
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(n, 0).UTC(), true
	}
	t, err := time.Parse(time.RFC3339, v)
	return t.UTC(), err == nil
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	var query audit.Query
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &query.From}, {"to", &query.To}, {"before", &query.Before}} {
		t, ok := parseWhen(q.Get(f.name))
		if !ok {
			return apierr.Validation(apierr.M("history_param", "{name} must be an integer", "name", f.name), nil)
		}
		*f.dst = t
	}
	query.Actor, query.Action, query.Target = q.Get("actor"), q.Get("action"), q.Get("target")
	query.Result, query.Text = q.Get("result"), q.Get("q")
	if n, err := strconv.Atoi(q.Get("limit")); err == nil {
		query.Limit = n
	}
	csv := q.Get("format") == "csv"
	if csv && query.Limit == 0 {
		query.Limit = audit.MaxLimit
	}
	entries, err := s.d.Audit.Query(query)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []audit.Entry{}
	}
	if !csv {
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
		return nil
	}
	// The export is itself an operation worth recording.
	s.audit(r, "audit.export", audit.Target{Type: "audit"}, map[string]any{"rows": len(entries)})
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []string{
			e.Time.Format(time.RFC3339), e.Actor.Type, e.Actor.Name, e.Actor.IP, e.Action,
			e.Target.Type, e.Target.ID, e.Target.Name, e.Result, e.Message,
		})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	return export.Write(w, []string{"time", "actor_type", "actor", "ip", "action", "target_type", "target_id", "target", "result", "message"}, rows)
}
