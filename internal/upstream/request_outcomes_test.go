package upstream

import (
	"testing"
	"time"
)

func TestUserOutcomesDeduplicateAttemptsAndKeepUnknownStreams(t *testing.T) {
	g := int64(1)
	yes := true
	now := time.Now()
	records := []TrafficRecord{{GroupID: &g, RequestID: "retried", Kind: "error", StatusCode: 503, CreatedAt: now}, {GroupID: &g, RequestID: "retried", Kind: "success", StreamComplete: &yes, CreatedAt: now}, {GroupID: &g, RequestID: "retried", Kind: "success", StreamComplete: &yes, CreatedAt: now}, {GroupID: &g, RequestID: "failed", Kind: "error", IsFinal: &yes, CreatedAt: now}, {GroupID: &g, RequestID: "stream", Kind: "success", Stream: &yes, CreatedAt: now}, {Kind: "error", CreatedAt: now}}
	result, missing := FinalRequestOutcomes(records)
	statuses := map[string]string{}
	for _, v := range result {
		statuses[v.RequestID] = v.Outcome
	}
	if len(result) != 3 || missing != 1 || statuses["retried"] != "success" || statuses["failed"] != "failure" || statuses["stream"] != "unknown" {
		t.Fatal(result, missing)
	}
	if MergeOutcome("success", "failure") != "conflict" || MergeOutcome("success", "unknown") != "success" {
		t.Fatal("outcome evidence was overwritten")
	}
}
