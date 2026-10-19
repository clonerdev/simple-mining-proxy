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
