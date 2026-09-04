package app

import "testing"

func float(value float64) *float64 { return &value }
func integer(value int) *int       { return &value }

func TestBuildReconcilePlanDenseGlobalOrdering(t *testing.T) {
	accounts := []reconcileAccount{
		{RemoteID: 3, Priority: 90, Rate: float(1), PriorityEnabled: true},
		{RemoteID: 1, Priority: 90, Rate: float(0.5), PriorityEnabled: true},
		{RemoteID: 2, Priority: 90, Rate: float(0.5), PriorityEnabled: true},
		{RemoteID: 4, Priority: 7, Rate: float(0.1), PriorityEnabled: false},
	}
	buildReconcilePlan(accounts, 1, 10)
	if accounts[0].Desired != 11 || accounts[1].Desired != 1 || accounts[2].Desired != 1 {
		t.Fatalf("unexpected dense priorities: %#v", accounts)
	}
	if accounts[3].Desired != 7 {
		t.Fatalf("disabled account priority changed: %#v", accounts[3])
	}
}

func TestBuildReconcilePlanGroupGuardOperators(t *testing.T) {
	groupRate := float(1)
	accounts := []reconcileAccount{
		{Priority: 1, Rate: float(1), GuardEnabled: true, GuardOperator: "gte", GuardPriority: 999, Groups: []GroupSummary{{RateMultiplier: groupRate}}},
		{Priority: 2, Rate: float(1), GuardEnabled: true, GuardOperator: "gt", GuardPriority: 998, Groups: []GroupSummary{{RateMultiplier: groupRate}}},
		{Priority: 999, Rate: float(0.5), GuardHolding: true, RestorePriority: integer(4)},
	}
	buildReconcilePlan(accounts, 1, 1)
	if !accounts[0].Violation || accounts[0].Desired != 999 {
		t.Fatalf("GTE guard was not applied: %#v", accounts[0])
	}
	if accounts[1].Violation || accounts[1].Desired != 2 {
		t.Fatalf("GT guard incorrectly matched equality: %#v", accounts[1])
	}
	if accounts[2].Desired != 4 || accounts[2].Violation {
		t.Fatalf("fixed priority was not restored: %#v", accounts[2])
	}
}

func TestBuildReconcilePlanGuardOverridesSorting(t *testing.T) {
	accounts := []reconcileAccount{{
		Priority: 50, Rate: float(2), PriorityEnabled: true, GuardEnabled: true,
		GuardOperator: "gt", GuardPriority: 999, Groups: []GroupSummary{{RateMultiplier: float(1)}},
	}}
	buildReconcilePlan(accounts, 1, 1)
	if accounts[0].Desired != 999 || !accounts[0].Violation {
		t.Fatalf("guard must override sorted priority: %#v", accounts[0])
	}
}
