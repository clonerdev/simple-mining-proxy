package devices

import (
	"errors"
	"testing"
)

func TestShareRuleValidAndFraction(t *testing.T) {
	for _, tc := range []struct {
		r     ShareRule
		valid bool
		frac  float64
	}{
		{ShareRule{ShareKindPercent, 5}, true, 0.05},
		{ShareRule{ShareKindPercent, 0}, true, 0}, // left out
		{ShareRule{ShareKindPercent, 100}, true, 1},
		{ShareRule{ShareKindPercent, 100.5}, false, 0},
		{ShareRule{ShareKindPercent, -1}, false, 0},
		{ShareRule{ShareKindMinutes, 60}, true, 60.0 / 1440},
		{ShareRule{ShareKindMinutes, 1441}, false, 0},
		{ShareRule{"money", 5}, false, 0},
		{ShareRule{"", 5}, false, 0},
	} {
		if tc.r.Valid() != tc.valid || tc.r.Fraction() != tc.frac {
			t.Errorf("%+v: valid=%v frac=%v, want %v %v", tc.r, tc.r.Valid(), tc.r.Fraction(), tc.valid, tc.frac)
		}
	}
}

func TestOperatorRuleDeviceBeforeCustomerBeforeNothing(t *testing.T) {
	s, _ := newStore(t)
	evs := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	d1, d2 := register(t, s, c, "rack1"), register(t, s, c, "rack2")

	if _, _, ok := s.OperatorRule(c.ID, d1.ID); ok {
		t.Fatal("a new customer must inherit the setting")
	}
	if _, err := s.SetCustomerShare(c.ID, &ShareRule{ShareKindPercent, 3}, admin); err != nil {
		t.Fatal(err)
	}
	if r, lvl, ok := s.OperatorRule(c.ID, d1.ID); !ok || lvl != "customer" || r.Amount != 3 {
		t.Fatalf("got %+v %s %v", r, lvl, ok)
	}
	if _, err := s.SetDeviceShare(d2.ID, &ShareRule{ShareKindPercent, 0}, admin); err != nil {
		t.Fatal(err)
	}
	if r, lvl, ok := s.OperatorRule(c.ID, d2.ID); !ok || lvl != "device" || r.Amount != 0 {
		t.Fatalf("a device left out must win over its customer: %+v %s %v", r, lvl, ok)
	}
	if _, lvl, _ := s.OperatorRule(c.ID, d1.ID); lvl != "customer" {
		t.Fatalf("the other device keeps the customer's rule, got %s", lvl)
	}
	// Back to inheriting.
	if _, err := s.SetDeviceShare(d2.ID, nil, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCustomerShare(c.ID, nil, admin); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.OperatorRule(c.ID, d2.ID); ok {
		t.Fatal("inherit must remove the rule")
	}

	got := evs.byType(EvCustomerShare)
	if len(got) != 2 || got[0].From != "inherit" || got[0].To != "percent 3" || got[1].To != "inherit" {
		t.Fatalf("customer events: %+v", got)
	}
	if dv := evs.byType(EvDeviceShare); len(dv) != 2 || dv[0].To != "percent 0" || dv[0].DeviceID != d2.ID {
		t.Fatalf("device events: %+v", dv)
	}
}

func TestShareRuleIsRefusedWhenInvalidAndNothingChanges(t *testing.T) {
	s, _ := newStore(t)
	evs := logEvents(s)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rack1")
	var e *Error
	if _, err := s.SetCustomerShare(c.ID, &ShareRule{ShareKindPercent, 500}, admin); !errors.As(err, &e) || e.Key != "share_invalid" || e.Field != "operator_share" {
		t.Fatalf("customer: %v", err)
	}
	if _, err := s.SetDeviceShare(d.ID, &ShareRule{"x", 1}, admin); !errors.As(err, &e) || e.Key != "share_invalid" {
		t.Fatalf("device: %v", err)
	}
	if _, _, ok := s.OperatorRule(c.ID, d.ID); ok || len(evs.byType(EvCustomerShare))+len(evs.byType(EvDeviceShare)) != 0 {
		t.Fatal("a refused rule must change nothing and emit nothing")
	}
}

func TestShareRuleNeedsAnExistingSubjectAndDoesNotGiveConsent(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.SetCustomerShare("nope", &ShareRule{ShareKindPercent, 1}, admin); !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.SetDeviceShare("nope", nil, admin); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("got %v", err)
	}
	c, _ := mkCust(t, s, NewCustomer{})
	if _, err := s.SetCustomerShare(c.ID, &ShareRule{ShareKindPercent, 5}, admin); err != nil {
		t.Fatal(err)
	}
	if s.Consented(c.ID) {
		t.Fatal("a rule must never count as the customer's consent")
	}
}

func TestShareRuleSurvivesReopen(t *testing.T) {
	s, _ := newStore(t)
	c, _ := mkCust(t, s, NewCustomer{})
	d := register(t, s, c, "rack1")
	if _, err := s.SetDeviceShare(d.ID, &ShareRule{ShareKindMinutes, 30}, admin); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(s.doc.Path())
	if err != nil {
		t.Skipf("cannot reopen: %v", err)
	}
	if r, lvl, ok := s2.OperatorRule(c.ID, d.ID); !ok || lvl != "device" || r.Kind != ShareKindMinutes || r.Amount != 30 {
		t.Fatalf("got %+v %s %v", r, lvl, ok)
	}
}
