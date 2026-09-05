package upstream

import (
	"strconv"
	"time"
)

type RequestOutcome struct {
	GroupID   int64     `json:"group_id"`
	RequestID string    `json:"request_id"`
	Model     string    `json:"model"`
	Outcome   string    `json:"outcome"`
	SeenAt    time.Time `json:"seen_at"`
}

func MergeOutcome(old, next string) string {
	if old == "conflict" || next == "conflict" {
		return "conflict"
	}
	if old == "" || old == "unknown" {
		return next
	}
	if next == "unknown" || old == next {
		return old
	}
	return "conflict"
}
func FinalRequestOutcomes(records []TrafficRecord) ([]RequestOutcome, int) {
	byRequest := map[string]RequestOutcome{}
	uncorrelated := 0
	for _, r := range records {
		// A supplier attempt can fail even when a later account completes the request.
		if r.Source == trafficUpstreamErrors {
			continue
		}
		if r.GroupID == nil || *r.GroupID <= 0 || r.RequestID == "" {
			uncorrelated++
			continue
		}
		outcome := "unknown"
		if r.Source == trafficRequestErrors {
			outcome = "failure"
		} else {
			switch r.FinalOutcome {
			case "success", "failure":
				outcome = r.FinalOutcome
			}
		}
		if outcome == "unknown" && r.IsFinal != nil && *r.IsFinal {
			if r.Kind == "error" || r.StatusCode >= 400 || r.StreamComplete != nil && !*r.StreamComplete {
				outcome = "failure"
			} else if r.Kind == "success" {
				outcome = "success"
			}
		}
		if outcome == "unknown" && r.Kind == "success" {
			if r.StreamComplete != nil && *r.StreamComplete || r.Stream != nil && !*r.Stream {
				outcome = "success"
			}
		}
		key := strconv.FormatInt(*r.GroupID, 10) + "/" + r.RequestID
		old, ok := byRequest[key]
		if !ok {
			old = RequestOutcome{GroupID: *r.GroupID, RequestID: r.RequestID, Model: r.Model}
		}
		old.Outcome = MergeOutcome(old.Outcome, outcome)
		if outcome == "success" || outcome == "failure" {
			old.Model = r.Model
		}
		if r.CreatedAt.After(old.SeenAt) {
			old.SeenAt = r.CreatedAt
		}
		byRequest[key] = old
	}
	result := make([]RequestOutcome, 0, len(byRequest))
	for _, v := range byRequest {
		result = append(result, v)
	}
	return result, uncorrelated
}
