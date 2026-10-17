package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/famfamfam/simple-mining-proxy/internal/accounts"
	"github.com/famfamfam/simple-mining-proxy/internal/audit"
)

const userPass = "operator password 1"

type accEnv struct {
	*env
	acc *accounts.Store
	aud *audit.Log
}

func newAccEnv(t *testing.T) *accEnv {
	t.Helper()
	t.Cleanup(accounts.SetCost(2000))
	dir := t.TempDir()
	acc, err := accounts.Open(filepath.Join(dir, "accounts.json"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	aud, err := audit.Open(filepath.Join(dir, "audit"))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, func(d *Deps) { d.Accounts, d.Audit = acc, aud })
	return &accEnv{env: e, acc: acc, aud: aud}
}

// create makes an account through the API as the environment administrator.
func (e *accEnv) create(t *testing.T, body string) map[string]any {
	t.Helper()
	w := e.do("POST", "/api/users", body, bearer)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body)
	}
	var v map[string]any
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}

// login signs in and returns the headers that carry the session.
func (e *accEnv) login(t *testing.T, user, pass string) map[string]string {
	t.Helper()
	w := e.do("POST", "/api/auth/login", `{"username":"`+user+`","password":"`+pass+`"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login %s: %d %s", user, w.Code, w.Body)
	}
	c := w.Result().Cookies()[0]
	return map[string]string{"Cookie": c.Name + "=" + c.Value}
}

func TestOperatorPermissionsAreEnforcedByTheServer(t *testing.T) {
	e := newAccEnv(t)
	e.create(t, `{"username":"olga","role":"operator","password":"`+userPass+`"}`)
	op := e.login(t, "olga", userPass)

	allowed := [][2]string{{"GET", "/api/status"}, {"GET", "/api/miners"}, {"GET", "/api/events"}, {"GET", "/api/pools"}, {"GET", "/api/settings"}, {"GET", "/api/auth/me"}}
	for _, a := range allowed {
		if w := e.do(a[0], a[1], "", op); w.Code != 200 {
			t.Errorf("operator %s %s: %d, want 200", a[0], a[1], w.Code)
		}
	}
	denied := [][3]string{
		{"PUT", "/api/settings", `{"switch_drain":"20s"}`},
		{"POST", "/api/pools", `{"name":"x"}`},
		{"DELETE", "/api/pools/x", "{}"},
		{"POST", "/api/pools/x/activate", `{}`},
		{"POST", "/api/miners/reconnect", `{}`},
		{"PUT", "/api/telegram/token", `{"token":""}`},
		{"GET", "/api/users", ""},
		{"POST", "/api/users", `{"username":"mallory","role":"superadmin","password":"` + userPass + `"}`},
		{"GET", "/api/audit", ""},
	}
	for _, d := range denied {
		if w := e.do(d[0], d[1], d[2], op); w.Code != http.StatusForbidden {
			t.Errorf("operator %s %s: %d, want 403 (%s)", d[0], d[1], w.Code, w.Body)
		}
	}
	if e.set.Get().SwitchDrain.Seconds() != 10 {
		t.Fatal("a forbidden settings change was applied")
	}
	// No credentials at all is 401, not 403.
	if w := e.do("GET", "/api/users", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}

	// The refusals are in the audit log.
	got, _ := e.aud.Query(audit.Query{Action: "access.denied", Limit: 50})
	if len(got) < len(denied) {
		t.Fatalf("%d refusals audited, want at least %d", len(got), len(denied))
	}
	if got[0].Actor.Name != "olga" || got[0].Result != audit.Denied {
		t.Fatalf("audit entry: %+v", got[0])
	}
}

func TestGrantAndRevoke(t *testing.T) {
	e := newAccEnv(t)
	u := e.create(t, `{"username":"gina","role":"operator","grant":["audit.view"],"revoke":["events.view"],"password":"`+userPass+`"}`)
	op := e.login(t, "gina", userPass)
	if w := e.do("GET", "/api/audit", "", op); w.Code != 200 {
		t.Fatalf("granted audit.view: %d", w.Code)
	}
	if w := e.do("GET", "/api/events", "", op); w.Code != 403 {
		t.Fatalf("revoked events.view: %d", w.Code)
	}
	// Changing the permissions applies to the open session at once.
	if w := e.do("PUT", "/api/users/"+u["id"].(string), `{"revoke":[]}`, bearer); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.do("GET", "/api/events", "", op); w.Code != 200 {
		t.Fatalf("after un-revoke: %d", w.Code)
	}
}

func TestDisabledAccountLosesItsSessionAtOnce(t *testing.T) {
	e := newAccEnv(t)
	u := e.create(t, `{"username":"hank","role":"operator","password":"`+userPass+`"}`)
	op := e.login(t, "hank", userPass)
	if w := e.do("GET", "/api/status", "", op); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := e.do("PUT", "/api/users/"+u["id"].(string), `{"disabled":true}`, bearer); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.do("GET", "/api/status", "", op); w.Code != 401 {
		t.Fatalf("disabled account's session still works: %d", w.Code)
	}
	if w := e.do("POST", "/api/auth/login", `{"username":"hank","password":"`+userPass+`"}`, nil); w.Code != 401 {
		t.Fatalf("disabled account signed in: %d", w.Code)
	}
	if w := e.do("PUT", "/api/users/"+u["id"].(string), `{"disabled":false}`, bearer); w.Code != 200 {
		t.Fatal(w.Body)
	}
	e.login(t, "hank", userPass)
}

func TestPasswordResetEndsSessions(t *testing.T) {
	e := newAccEnv(t)
	u := e.create(t, `{"username":"ivan","role":"operator","password":"`+userPass+`"}`)
	op := e.login(t, "ivan", userPass)
	if w := e.do("POST", "/api/users/"+u["id"].(string)+"/password", `{"password":"another long password"}`, bearer); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := e.do("GET", "/api/status", "", op); w.Code != 401 {
		t.Fatalf("old session after a password reset: %d", w.Code)
	}
	e.login(t, "ivan", "another long password")
	if w := e.do("POST", "/api/auth/login", `{"username":"ivan","password":"`+userPass+`"}`, nil); w.Code != 401 {
		t.Fatalf("old password: %d", w.Code)
	}
}

func TestChangeOwnPasswordKeepsTheSession(t *testing.T) {
	e := newAccEnv(t)
	e.create(t, `{"username":"jane","role":"operator","password":"`+userPass+`"}`)
	op := e.login(t, "jane", userPass)
	if w := e.do("POST", "/api/auth/password", `{"current_password":"wrong wrong wrong","new_password":"a new long password"}`, op); w.Code != 400 {
		t.Fatalf("wrong current password: %d %s", w.Code, w.Body)
	}
	w := e.do("POST", "/api/auth/password", `{"current_password":"`+userPass+`","new_password":"a new long password"}`, op)
	if w.Code != 200 {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	fresh := map[string]string{"Cookie": w.Result().Cookies()[0].Name + "=" + w.Result().Cookies()[0].Value}
	if w := e.do("GET", "/api/status", "", fresh); w.Code != 200 {
		t.Fatalf("the reissued session: %d", w.Code)
	}
	if w := e.do("GET", "/api/status", "", op); w.Code != 401 {
		t.Fatalf("the old session after the change: %d", w.Code)
	}
	// The environment administrator's password is not changed here.
	env := e.login(t, "admin", "secret")
	if w := e.do("POST", "/api/auth/password", `{"current_password":"secret","new_password":"a new long password"}`, env); w.Code != 409 {
		t.Fatalf("env admin password change: %d", w.Code)
	}
}

func TestNoEscalation(t *testing.T) {
	e := newAccEnv(t)
	e.create(t, `{"username":"kate","role":"operator","grant":["users.manage"],"password":"`+userPass+`"}`)
	kate := e.login(t, "kate", userPass)

	// She may manage users, but cannot hand out what she does not have.
	for _, body := range []string{
		`{"username":"root2","role":"superadmin","password":"` + userPass + `"}`,
		`{"username":"peer","role":"operator","grant":["settings.edit"],"password":"` + userPass + `"}`,
	} {
		if w := e.do("POST", "/api/users", body, kate); w.Code != 403 {
			t.Errorf("escalating create %s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := e.do("POST", "/api/users", `{"username":"ok.user","role":"operator","password":"`+userPass+`"}`, kate); w.Code != 201 {
		t.Fatalf("plain create: %d %s", w.Code, w.Body)
	}
	// And she cannot touch a super administrator.
	sa := e.create(t, `{"username":"boss","role":"superadmin","password":"`+userPass+`"}`)
	id := sa["id"].(string)
	if w := e.do("PUT", "/api/users/"+id, `{"disabled":true}`, kate); w.Code != 403 {
		t.Errorf("disabling a super administrator: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/users/"+id, "{}", kate); w.Code != 403 {
		t.Errorf("deleting a super administrator: %d", w.Code)
	}
	if w := e.do("POST", "/api/users/"+id+"/password", `{"password":"takeover password 1"}`, kate); w.Code != 403 {
		t.Errorf("resetting a super administrator's password: %d", w.Code)
	}
	// Nobody edits their own access.
	kid := e.acc.List()
	var kateID string
	for _, u := range kid {
		if u.Username == "kate" {
			kateID = u.ID
		}
	}
	if w := e.do("PUT", "/api/users/"+kateID, `{"grant":["settings.edit","users.manage"]}`, kate); w.Code != 409 {
		t.Errorf("self-promotion: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/users/"+kateID, "{}", kate); w.Code != 409 {
		t.Errorf("self-delete: %d", w.Code)
	}
}

func TestCustomerSeesNothingOfTheAdministration(t *testing.T) {
	e := newAccEnv(t)
	e.create(t, `{"username":"cust1","role":"customer","customer_id":"c-1","password":"`+userPass+`"}`)
	c := e.login(t, "cust1", userPass)
	var me struct {
		Role        string   `json:"role"`
		Permissions []string `json:"permissions"`
		CustomerID  string   `json:"customer_id"`
	}
	w := e.do("GET", "/api/auth/me", "", c)
	json.Unmarshal(w.Body.Bytes(), &me)
	if w.Code != 200 || me.Role != "customer" || len(me.Permissions) != 1 || me.Permissions[0] != "self.view" || me.CustomerID != "c-1" {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/api/status", "/api/miners", "/api/events", "/api/pools", "/api/settings", "/api/users", "/api/audit", "/api/history", "/api/network"} {
		if w := e.do("GET", p, "", c); w.Code != 403 {
			t.Errorf("customer GET %s: %d, want 403", p, w.Code)
		}
	}
}

func TestSecretsNeverLeak(t *testing.T) {
	e := newAccEnv(t)
	e.create(t, `{"username":"lena","role":"operator","password":"`+userPass+`"}`)
	e.login(t, "lena", userPass)
	list := e.do("GET", "/api/users", "", bearer).Body.String()
	log := e.do("GET", "/api/audit", "", bearer).Body.String()
	csv := e.do("GET", "/api/audit?format=csv", "", bearer).Body.String()
	for name, body := range map[string]string{"users": list, "audit": log, "csv": csv} {
		for _, secret := range []string{userPass, "pbkdf2", `"hash"`} {
			if strings.Contains(body, secret) {
				t.Errorf("%s response contains %q", name, secret)
			}
		}
	}
	if !strings.Contains(log, "user.create") || !strings.Contains(log, "auth.login") {
		t.Fatalf("audit log misses the operations: %s", log)
	}
	if !strings.Contains(csv, "user.create") || !strings.HasPrefix(csv, "\uFEFF") {
		t.Fatalf("csv export: %q", csv)
	}
}

func TestEnvAdminAndTokenStillWork(t *testing.T) {
	e := newAccEnv(t)
	root := e.login(t, "admin", "secret")
	for _, h := range []map[string]string{root, bearer} {
		for _, p := range []string{"/api/status", "/api/settings", "/api/users", "/api/audit"} {
			if w := e.do("GET", p, "", h); w.Code != 200 {
				t.Errorf("GET %s: %d", p, w.Code)
			}
		}
	}
	// An account cannot take the name of the environment administrator.
	if w := e.do("POST", "/api/users", `{"username":"admin","role":"operator","password":"`+userPass+`"}`, bearer); w.Code != 400 {
		t.Fatalf("reserved name: %d", w.Code)
	}
}
