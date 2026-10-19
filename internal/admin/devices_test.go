package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/accounts"
	"github.com/famfamfam/simple-mining-proxy/internal/audit"
	"github.com/famfamfam/simple-mining-proxy/internal/devices"
	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
)

type devEnv struct {
	*accEnv
	ds  *devices.Store
	ops *opshare.Router
}

func newDevEnv(t *testing.T) *devEnv {
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
	ds, err := devices.Open(filepath.Join(dir, "devices.json"))
	if err != nil {
		t.Fatal(err)
	}
	ops := opshare.NewRouter(filepath.Join(dir, "opshare"), func() opshare.Config { return opshare.Config{Enabled: true} },
		opshare.FlatRules{Fraction: func() float64 { return 0.02 }}, ds)
	e := newEnv(t, func(d *Deps) {
		d.Accounts, d.Audit, d.Devices, d.OperatorShare = acc, aud, ds, ops
		(&devices.Enforcement{Store: ds, Registry: d.Registry, Audit: aud, Events: d.Events}).Install()
	})
	return &devEnv{accEnv: &accEnv{env: e, acc: acc, aud: aud}, ds: ds, ops: ops}
}

// customerVia creates a customer through the API and returns its view and key.
func (e *devEnv) customerVia(t *testing.T, hdr map[string]string, body string) (map[string]any, string) {
	t.Helper()
	w := e.do("POST", "/api/customers", body, hdr)
	if w.Code != http.StatusCreated {
		t.Fatalf("create customer: %d %s", w.Code, w.Body)
	}
	var out struct {
		Customer map[string]any `json:"customer"`
		Key      string         `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Customer, out.Key
}

// pending registers a device the way an ASIC's first login does.
func (e *devEnv) pending(t *testing.T, customerID, worker string) devices.Device {
	t.Helper()
	c, ok := e.ds.Customer(customerID)
	if !ok {
		t.Fatal("no customer")
	}
	d, _, err := e.ds.Register(c, worker, "bmminer", "10.0.0.1", 10)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestConnectionKeyIsShownOnceAndNeverStored(t *testing.T) {
	e := newDevEnv(t)
	c, key := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme","account":"acmebtc"}`)
	if len(key) < 16 {
		t.Fatalf("key %q is too short to be a key", key)
	}
	id := c["id"].(string)

	// Nothing the API says afterwards contains the key, its hash or its salt.
	for _, path := range []string{"/api/customers", "/api/customers/" + id, "/api/devices"} {
		body := e.do("GET", path, "", bearer).Body.String()
		for _, secret := range []string{key, "key_hash", "key_salt", "KeyHash", "KeySalt"} {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s leaks %q", path, secret)
			}
		}
	}
	// The audit log has no key either.
	entries, _ := e.aud.Query(audit.Query{Limit: 100})
	raw, _ := json.Marshal(entries)
	if strings.Contains(string(raw), key) {
		t.Fatal("the connection key is in the audit log")
	}
	if len(entries) == 0 {
		t.Fatal("creating a customer was not audited")
	}

	// Rotating shows a new key once, and the old one stops working.
	w := e.do("POST", "/api/customers/"+id+"/key", `{}`, bearer)
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var rot struct{ Key string }
	json.Unmarshal(w.Body.Bytes(), &rot)
	if rot.Key == "" || rot.Key == key {
		t.Fatalf("rotation returned %q", rot.Key)
	}
	cust, _ := e.ds.Customer(id)
	if devices.CheckKey(cust, key) {
		t.Fatal("the old key still works")
	}
	if !devices.CheckKey(cust, rot.Key) {
		t.Fatal("the new key does not work")
	}
}

func TestDeviceActionsThroughTheAPI(t *testing.T) {
	e := newDevEnv(t)
	c, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme","account":"acmebtc","max_devices":1}`)
	cid := c["id"].(string)
	d1, d2 := e.pending(t, cid, "rack1"), e.pending(t, cid, "rack2")

	act := func(id, body string) *httpResult {
		w := e.do("POST", "/api/devices/"+id+"/action", body, bearer)
		return &httpResult{w.Code, w.Body.String()}
	}
	if r := act(d1.ID, `{"action":"approve"}`); r.code != 200 {
		t.Fatalf("approve: %d %s", r.code, r.body)
	}
	// The customer's limit is one working device: a second approval is refused
	// with a conflict, not applied.
	r := act(d2.ID, `{"action":"approve"}`)
	if r.code != http.StatusConflict || !strings.Contains(r.body, "device_device_limit") {
		t.Fatalf("second approval: %d %s", r.code, r.body)
	}
	if d, _ := e.ds.Device(d2.ID); d.Status != devices.Pending {
		t.Fatalf("a refused approval changed the device to %s", d.Status)
	}
	// An action that does not fit the state is a conflict too.
	if r := act(d1.ID, `{"action":"unblock"}`); r.code != http.StatusConflict || !strings.Contains(r.body, "device_transition_denied") {
		t.Fatalf("unblock of an approved device: %d %s", r.code, r.body)
	}
	if r := act(d1.ID, `{"action":"explode"}`); r.code != http.StatusBadRequest || !strings.Contains(r.body, "device_action_invalid") {
		t.Fatalf("unknown action: %d %s", r.code, r.body)
	}
	if r := act("nope", `{"action":"block"}`); r.code != http.StatusNotFound {
		t.Fatalf("unknown device: %d %s", r.code, r.body)
	}
	for _, step := range []struct{ action, want string }{
		{"suspend", "suspended"}, {"resume", "approved"}, {"block", "blocked"}, {"unblock", "approved"}, {"revoke", "revoked"},
	} {
		r := act(d1.ID, `{"action":"`+step.action+`","reason":"test"}`)
		if r.code != 200 || !strings.Contains(r.body, `"status":"`+step.want+`"`) {
			t.Fatalf("%s: %d %s", step.action, r.code, r.body)
		}
	}
	// A revoked device stays on record, so the name cannot just register again.
	if d, _ := e.ds.Device(d1.ID); d.Status != devices.Revoked {
		t.Fatalf("status %s", d.Status)
	}
	got, _ := e.aud.Query(audit.Query{Action: devices.EvDeviceRevoked, Limit: 5})
	if len(got) != 1 || got[0].Actor.Type != audit.ActorAPIToken {
		t.Fatalf("revoke audit: %+v", got)
	}
}

type httpResult struct {
	code int
	body string
}

func TestDeviceAdministrationNeedsItsPermissions(t *testing.T) {
	e := newDevEnv(t)
	c, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme","account":"acmebtc"}`)
	cid := c["id"].(string)
	d := e.pending(t, cid, "rack1")

	e.create(t, `{"username":"olga","role":"operator","password":"`+userPass+`"}`)
	e.create(t, `{"username":"viewer","role":"operator","revoke":["devices.manage"],"password":"`+userPass+`"}`)
	op, viewer := e.login(t, "olga", userPass), e.login(t, "viewer", userPass)

	// An operator handles devices.
	if w := e.do("GET", "/api/devices", "", op); w.Code != 200 {
		t.Fatalf("operator list: %d", w.Code)
	}
	if w := e.do("POST", "/api/devices/"+d.ID+"/action", `{"action":"approve"}`, op); w.Code != 200 {
		t.Fatalf("operator approve: %d %s", w.Code, w.Body)
	}
	// Without devices.manage the account can look and not touch.
	if w := e.do("GET", "/api/devices", "", viewer); w.Code != 200 {
		t.Fatalf("viewer list: %d", w.Code)
	}
	for _, r := range [][3]string{
		{"POST", "/api/devices/" + d.ID + "/action", `{"action":"block"}`},
		{"PUT", "/api/devices/" + d.ID, `{"notes":"x"}`},
		{"DELETE", "/api/devices/" + d.ID, `{}`},
		{"POST", "/api/customers", `{"slug":"evil","name":"Evil"}`},
		{"POST", "/api/customers/" + cid + "/key", `{}`},
		{"POST", "/api/customers/" + cid + "/status", `{"status":"blocked"}`},
		{"DELETE", "/api/customers/" + cid, `{}`},
	} {
		if w := e.do(r[0], r[1], r[2], viewer); w.Code != http.StatusForbidden {
			t.Errorf("viewer %s %s: %d, want 403", r[0], r[1], w.Code)
		}
	}
	if d, _ := e.ds.Device(d.ID); d.Status != devices.Approved {
		t.Fatalf("a forbidden call changed the device to %s", d.Status)
	}
}

// Customers never use the panel: there is no customer account and no
// endpoint made for one. Everything about a customer is done by the
// administration.
func TestCustomersHaveNoAccessToThePanel(t *testing.T) {
	e := newDevEnv(t)
	c, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme"}`)
	cid := c["id"].(string)

	if w := e.do("POST", "/api/users", `{"username":"acme-user","role":"customer","customer_id":"`+cid+`","password":"`+userPass+`"}`, bearer); w.Code != http.StatusBadRequest {
		t.Fatalf("a customer account was created: %d %s", w.Code, w.Body)
	}
	for _, r := range [][2]string{
		{"GET", "/api/self"}, {"POST", "/api/self/fee-consent"}, {"GET", "/api/self/operator-share"},
	} {
		if w := e.do(r[0], r[1], `{}`, bearer); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", r[0], r[1], w.Code)
		}
	}
}

func TestCustomerValidationAndLimits(t *testing.T) {
	e := newDevEnv(t)
	e.customerVia(t, bearer, `{"slug":"acme","name":"Acme"}`)
	for _, tc := range []struct{ body, field string }{
		{`{"slug":"AC","name":"x"}`, "slug"},
		{`{"slug":"acme","name":"dup"}`, "slug"},
		{`{"slug":"fine","name":""}`, "name"},
		{`{"slug":"fine","name":"x","account":"bad account"}`, "account"},
		{`{"slug":"fine","name":"x","max_devices":-1}`, "max_devices"},
		{`{"slug":"fine","name":"x","expires_at":"yesterday"}`, "expires_at"},
		{`{"slug":"fine","name":"x","language":"xx"}`, "language"},
	} {
		w := e.do("POST", "/api/customers", tc.body, bearer)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400 (%s)", tc.body, w.Code, w.Body)
			continue
		}
		var out struct {
			Fields map[string]json.RawMessage `json:"fields"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		if _, ok := out.Fields[tc.field]; !ok {
			t.Errorf("%s: no error for field %q in %s", tc.body, tc.field, w.Body)
		}
	}
	// Unauthenticated callers get 401, never data.
	if w := e.do("GET", "/api/customers", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
}

func TestDeletingACustomerWithDevicesIsRefused(t *testing.T) {
	e := newDevEnv(t)
	c, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme"}`)
	id := c["id"].(string)
	d := e.pending(t, id, "rack1")
	if w := e.do("DELETE", "/api/customers/"+id, `{}`, bearer); w.Code != http.StatusConflict {
		t.Fatalf("delete with a device: %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", "/api/devices/"+d.ID, `{}`, bearer); w.Code != 200 {
		t.Fatalf("delete device: %d %s", w.Code, w.Body)
	}
	if w := e.do("DELETE", "/api/customers/"+id, `{}`, bearer); w.Code != 200 {
		t.Fatalf("delete customer: %d %s", w.Code, w.Body)
	}
}

// The administrator approves a device for a time: the approval and its end are
// one request, and a bad end changes nothing.
func TestApproveADeviceForAMonthThroughTheAPI(t *testing.T) {
	e := newDevEnv(t)
	c, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme"}`)
	cid := c["id"].(string)
	d := e.pending(t, cid, "rack1")

	var out struct {
		Status    string     `json:"status"`
		ExpiresAt *time.Time `json:"expires_at"`
		Usable    bool       `json:"usable"`
	}
	end := time.Now().UTC().AddDate(0, 1, 0).Truncate(time.Second)
	w := e.do("POST", "/api/devices/"+d.ID+"/action",
		`{"action":"approve","reason":"paid","expires_at":"`+end.Format(time.RFC3339)+`"}`, bearer)
	if w.Code != 200 {
		t.Fatalf("approve with an expiry: %d %s", w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Status != "approved" || out.ExpiresAt == nil || !out.ExpiresAt.Equal(end) || !out.Usable {
		t.Fatalf("approved device: %+v", out)
	}

	// Disable and enable it again, then block and allow it: the end stays.
	for _, a := range []string{"suspend", "resume", "block", "unblock"} {
		if w := e.do("POST", "/api/devices/"+d.ID+"/action", `{"action":"`+a+`"}`, bearer); w.Code != 200 {
			t.Fatalf("%s: %d %s", a, w.Code, w.Body)
		}
	}
	if got, _ := e.ds.Device(d.ID); got.Status != devices.Approved || got.ExpiresAt == nil || !got.ExpiresAt.Equal(end) {
		t.Fatalf("after suspend/resume/block/unblock: %s %v", got.Status, got.ExpiresAt)
	}

	// Extend it with the device update.
	longer := end.AddDate(0, 1, 0)
	if w := e.do("PUT", "/api/devices/"+d.ID, `{"expires_at":"`+longer.Format(time.RFC3339)+`"}`, bearer); w.Code != 200 {
		t.Fatalf("extend: %d %s", w.Code, w.Body)
	}
	if got, _ := e.ds.Device(d.ID); !got.ExpiresAt.Equal(longer) {
		t.Fatalf("not extended: %v", got.ExpiresAt)
	}

	// A bad end is a validation error on the field, and changes nothing.
	d2 := e.pending(t, cid, "rack2")
	for _, tc := range []struct{ body, field string }{
		{`{"action":"approve","expires_at":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `"}`, "expires_at"},
		{`{"action":"approve","expires_at":"next month"}`, "expires_at"},
		{`{"action":"block","expires_at":"` + longer.Format(time.RFC3339) + `"}`, "expires_at"},
	} {
		w := e.do("POST", "/api/devices/"+d2.ID+"/action", tc.body, bearer)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.field) {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
		if got, _ := e.ds.Device(d2.ID); got.Status != devices.Pending || got.ExpiresAt != nil {
			t.Fatalf("%s changed the device: %s %v", tc.body, got.Status, got.ExpiresAt)
		}
	}
}
