package health

import (
	"context"
	"fmt"
	"testing"
)

func intPointer(value int) *int { return &value }

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		name       string
		value      any
		reason     FailureReason
		httpStatus *int
	}{
		{
			name: "semantic balance wins over forbidden status",
			value: map[string]any{
				"status": 403,
				"error":  map[string]any{"code": "INSUFFICIENT_BALANCE", "message": "Insufficient account balance"},
			},
			reason:     FailureBalance,
			httpStatus: intPointer(403),
		},
		{
			name: "semantic balance wins over unauthorized status",
			value: map[string]any{
				"status": 401,
				"error":  map[string]any{"code": "insufficient_user_quota", "message": "quota exhausted"},
			},
			reason:     FailureBalance,
			httpStatus: intPointer(401),
		},
		{
			name: "semantic balance wins over rate limit status",
			value: map[string]any{
				"status": 429,
				"error":  map[string]any{"message": "user quota is not enough"},
			},
			reason:     FailureBalance,
			httpStatus: intPointer(429),
		},
		{
			name:       "forbidden authentication",
			value:      map[string]any{"response": map[string]any{"statusCode": "403"}, "message": "Forbidden"},
			reason:     FailureAuth,
			httpStatus: intPointer(403),
		},
		{
			name:       "rate limited",
			value:      map[string]any{"code": 429, "error": "Too many requests"},
			reason:     FailureRateLimit,
			httpStatus: intPointer(429),
		},
		{
			name:       "deadline timeout",
			value:      fmt.Errorf("Sub2API probe: %w", context.DeadlineExceeded),
			reason:     FailureTimeout,
			httpStatus: nil,
		},
		{
			name:       "invalid model configuration",
			value:      map[string]any{"status": 422, "error": map[string]any{"message": "model not found"}},
			reason:     FailureConfiguration,
			httpStatus: intPointer(422),
		},
		{
			name:       "upstream server error",
			value:      `API returned 503: service unavailable`,
			reason:     FailureUpstream,
			httpStatus: intPointer(503),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := ClassifyFailure(test.value)
			if classification.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", classification.Reason, test.reason)
			}
			if test.httpStatus == nil {
				if classification.HTTPStatus != nil {
					t.Fatalf("HTTPStatus = %d, want nil", *classification.HTTPStatus)
				}
			} else if classification.HTTPStatus == nil || *classification.HTTPStatus != *test.httpStatus {
				t.Fatalf("HTTPStatus = %v, want %d", classification.HTTPStatus, *test.httpStatus)
			}
		})
	}
}

func TestClassifyFailureExtractsStatusFromMessage(t *testing.T) {
	classification := ClassifyFailure(map[string]any{
		"message": `API returned 403: {"code":"INSUFFICIENT_BALANCE","message":"Insufficient account balance"}`,
	})
	if classification.Reason != FailureBalance || classification.HTTPStatus == nil || *classification.HTTPStatus != 403 {
		t.Fatalf("classification = %#v", classification)
	}
}
