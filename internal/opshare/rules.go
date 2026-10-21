package opshare

import "time"

// FlatRules gives every subject the same share, read live from the settings.
// It is the rule set of a proxy without a billing engine: one global rule.
// The billing engine replaces it with rules that have a scope, a priority and
// a validity, behind the same Rules interface.
type FlatRules struct {
	// Fraction returns the share of the mining time, 0 to 1.
	Fraction func() float64
}

// Share implements Rules.
func (f FlatRules) Share(Subject, time.Time) Decision {
	if f.Fraction == nil {
		return Decision{}
	}
	return Decision{Fraction: f.Fraction(), RuleID: "global"}
}

// Override is a rule of one device or one customer.
type Override struct {
	Fraction float64 // 0 to 1; zero leaves the subject out
	// Level is "device" or "customer": the scope the rule was set on.
	Level string
}

// Overrides finds the rule set on a device or on its customer, device first.
type Overrides interface {
	Override(sub Subject) (Override, bool)
}

// ScopedRules gives a device or a customer its own share where one was set,
// and everyone else the base rules (the proxy-wide setting). The rule that
// applies is the most specific one: the device's, then the customer's, then
// the base. The result is always capped by Config.MaxFraction in the Router,
// and the customer's consent is still checked on every share.
type ScopedRules struct {
	Base      Rules
	Overrides Overrides
}

// Share implements Rules.
func (r ScopedRules) Share(sub Subject, now time.Time) Decision {
	if r.Overrides != nil {
		if o, ok := r.Overrides.Override(sub); ok {
			f := o.Fraction
			if f < 0 {
				f = 0
			}
			return Decision{Fraction: f, RuleID: o.Level}
		}
	}
	if r.Base == nil {
		return Decision{}
	}
	return r.Base.Share(sub, now)
}
