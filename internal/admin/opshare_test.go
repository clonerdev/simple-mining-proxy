package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/opshare"
)

func TestOperatorShareAccountIsSeenByTheRightPeople(t *testing.T) {
	e := newDevEnv(t)
	a, _ := e.customerVia(t, bearer, `{"slug":"acme","name":"Acme","account":"acmebtc"}`)
	b, _ := e.customerVia(t, bearer, `{"slug":"globex","name":"Globex","account":"globexbtc"}`)
	aid, bid := a["id"].(string), b["id"].(string)
	da, db := e.pending(t, aid, "a-rack1"), e.pending(t, bid, "b-rack1")

	now := time.Now()
	e.ops.Note(opshare.Outcome{DeviceID: da.ID, CustomerID: aid, PoolID: "p", Operator: true, Window: 900, Accepted: true, Difficulty: 8, At: now})
	e.ops.Note(opshare.Outcome{DeviceID: da.ID, CustomerID: aid, PoolID: "p", Accepted: true, Difficulty: 392, At: now})
	e.ops.Note(opshare.Outcome{DeviceID: db.ID, CustomerID: bid, PoolID: "p", Accepted: true, Difficulty: 100, At: now})

	e.create(t, `{"username":"acme-user","role":"customer","customer_id":"`+aid+`","password":"`+userPass+`"}`)
	e.create(t, `{"username":"nobill","role":"operator","revoke":["billing.view"],"password":"`+userPass+`"}`)
	cust, nobill := e.login(t, "acme-user", userPass), e.login(t, "nobill", userPass)

	type usage struct {
		Rows []opshare.DayUsage `json:"rows"`
	}
	read := func(path string, hdr map[string]string) (usage, int) {
		w := e.do("GET", path, "", hdr)
		var u usage
		json.Unmarshal(w.Body.Bytes(), &u)
		return u, w.Code
	}

	// An administrator sees every customer, and can narrow it to one.
	if u, code := read("/api/operator-share/usage", bearer); code != 200 || len(u.Rows) != 2 {
		t.Fatalf("admin usage: %d, %d rows", code, len(u.Rows))
	}
	if u, _ := read("/api/operator-share/usage?customer="+aid, bearer); len(u.Rows) != 1 || u.Rows[0].CustomerID != aid {
		t.Fatalf("filtered usage: %+v", u.Rows)
	}
	u, _ := read("/api/operator-share/usage?customer="+aid, bearer)
	if r := u.Rows[0]; r.OperatorAccepted != 1 || r.CustomerAccepted != 1 || r.OperatorShareOfWork() != 0.02 {
		t.Fatalf("account of the device: %+v (operator share of work %v)", r, r.OperatorShareOfWork())
	}

	// A customer sees only their own account; neither a parameter nor a
	// foreign id changes whose it is.
	own, code := read("/api/self/operator-share?customer="+bid+"&customer_id="+bid, cust)
	if code != 200 || len(own.Rows) != 1 || own.Rows[0].CustomerID != aid {
		t.Fatalf("customer's own account: %d %+v", code, own.Rows)
	}
	if body := e.do("GET", "/api/self/operator-share", "", cust).Body.String(); strings.Contains(body, bid) || strings.Contains(body, "globex") {
		t.Fatalf("the customer's account names another customer: %s", body)
	}

	// A customer cannot read the operator's view, and an operator whose
	// billing permission was revoked cannot either.
	for _, p := range []string{"/api/operator-share/usage", "/api/operator-share/pools"} {
		if w := e.do("GET", p, "", cust); w.Code != http.StatusForbidden {
			t.Errorf("customer %s: %d, want 403", p, w.Code)
		}
	}
	if _, code := read("/api/operator-share/usage", nobill); code != http.StatusForbidden {
		t.Errorf("operator without billing.view: %d, want 403", code)
	}
	if _, code := read("/api/self/operator-share", nobill); code != http.StatusForbidden {
		t.Errorf("staff account on the customer's own endpoint: %d, want 403", code)
	}
}

func TestOperatorShareUsageRejectsBadDates(t *testing.T) {
	e := newDevEnv(t)
	for _, q := range []string{"?from=yesterday", "?to=2026-13-40", "?from=2026-10-10&to=2026-10-01"} {
		if w := e.do("GET", "/api/operator-share/usage"+q, "", bearer); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, w.Code)
		}
	}
	if w := e.do("GET", "/api/operator-share/usage?from=2026-10-01&to=2026-10-02", "", bearer); w.Code != 200 {
		t.Fatalf("a valid range: %d %s", w.Code, w.Body)
	}
}

func TestOperatorSharePoolsListsWhatSwitchedOff(t *testing.T) {
	e := newDevEnv(t)
	e.ops.OperatorRefused("pool1", "unauthorized worker")
	w := e.do("GET", "/api/operator-share/pools", "", bearer)
	var out struct {
		Active bool                 `json:"active"`
		Off    []opshare.PoolStatus `json:"pools_off"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 || !out.Active || len(out.Off) != 1 || out.Off[0].PoolID != "pool1" || !strings.Contains(out.Off[0].Reason, "unauthorized worker") {
		t.Fatalf("pools: %d %s", w.Code, w.Body)
	}
}
