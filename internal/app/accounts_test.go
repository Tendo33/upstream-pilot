package app

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestCalculateUptimePercent(t *testing.T) {
	tests := []struct {
		name      string
		successes int
		total     int
		want      *float64
	}{
		{name: "no persisted probes", successes: 0, total: 0},
		{name: "all successful", successes: 3, total: 3, want: float64Pointer(100)},
		{name: "partial success", successes: 2, total: 3, want: float64Pointer(66.7)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := calculateUptimePercent(test.successes, test.total)
			if test.want == nil {
				if got != nil {
					t.Fatalf("calculateUptimePercent() = %v, want nil", *got)
				}
				return
			}
			if got == nil || math.Abs(*got-*test.want) > 1e-12 {
				t.Fatalf("calculateUptimePercent() = %v, want %v", got, *test.want)
			}
		})
	}
}

func TestAccountUptimePercentSerializesNullWithoutSamples(t *testing.T) {
	encoded, err := json.Marshal(Account{})
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	value, exists := response["uptime_percent"]
	if !exists || value != nil {
		t.Fatalf("uptime_percent = %#v (exists=%t), want explicit null", value, exists)
	}
}

func TestNormalizeBulkAccountIDs(t *testing.T) {
	first := "00000000-0000-4000-8000-000000000001"
	second := "00000000-0000-4000-8000-000000000002"
	ids, apiErr := normalizeBulkAccountIDs([]string{" " + first + " ", second, first})
	if apiErr != nil {
		t.Fatalf("normalizeBulkAccountIDs: %v", apiErr)
	}
	if len(ids) != 2 || ids[0] != first || ids[1] != second {
		t.Fatalf("ids = %#v", ids)
	}

	if _, apiErr := normalizeBulkAccountIDs(nil); apiErr == nil || apiErr.Code != "ACCOUNT_IDS_REQUIRED" {
		t.Fatalf("empty IDs error = %#v", apiErr)
	}
	if _, apiErr := normalizeBulkAccountIDs([]string{"invalid"}); apiErr == nil || apiErr.Code != "INVALID_ACCOUNT_ID" {
		t.Fatalf("invalid ID error = %#v", apiErr)
	}
	tooMany := make([]string, maxBulkAccountIDs+1)
	if _, apiErr := normalizeBulkAccountIDs(tooMany); apiErr == nil || apiErr.Code != "TOO_MANY_ACCOUNTS" {
		t.Fatalf("too many IDs error = %#v", apiErr)
	}
}

func TestValidateBulkAccountSettings(t *testing.T) {
	tests := []struct {
		name     string
		input    bulkAccountSettingsInput
		wantCode string
	}{
		{name: "no selected settings", wantCode: "SETTINGS_REQUIRED"},
		{name: "priority can be disabled", input: bulkAccountSettingsInput{Priority: &bulkAccountPrioritySettings{Enabled: false}}},
		{
			name: "valid health settings",
			input: bulkAccountSettingsInput{Health: &bulkAccountHealthSettings{
				Enabled: true, ProbeIntervalSeconds: 10, ProbeTimeoutSeconds: 3, FailureThreshold: 1, RecoverySuccessThreshold: 3, ProbeModel: " model ",
			}},
		},
		{
			name: "invalid health settings",
			input: bulkAccountSettingsInput{Health: &bulkAccountHealthSettings{
				ProbeIntervalSeconds: 9, ProbeTimeoutSeconds: 3, FailureThreshold: 1, RecoverySuccessThreshold: 1,
			}},
			wantCode: "INVALID_PROBE_SETTINGS",
		},
		{
			name: "invalid recovery success threshold",
			input: bulkAccountSettingsInput{Health: &bulkAccountHealthSettings{
				ProbeIntervalSeconds: 10, ProbeTimeoutSeconds: 3, FailureThreshold: 1, RecoverySuccessThreshold: 101,
			}},
			wantCode: "INVALID_PROBE_SETTINGS",
		},
		{
			name:     "invalid rate interval",
			input:    bulkAccountSettingsInput{RateSync: &bulkAccountRateSyncSettings{IntervalSeconds: 29}},
			wantCode: "INVALID_RATE_INTERVAL",
		},
		{
			name:  "guard operator is normalized",
			input: bulkAccountSettingsInput{Guard: &bulkAccountGuardSettings{Operator: " GTE ", Priority: 1_000_000}},
		},
		{
			name:     "invalid guard priority",
			input:    bulkAccountSettingsInput{Guard: &bulkAccountGuardSettings{Operator: "gt", Priority: -1}},
			wantCode: "INVALID_GUARD_PRIORITY",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateBulkAccountSettings(&test.input)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("validateBulkAccountSettings: %v", err)
				}
				if test.input.Health != nil && test.input.Health.ProbeModel != "model" {
					t.Fatalf("probe model = %q, want normalized model", test.input.Health.ProbeModel)
				}
				if test.input.Guard != nil && test.input.Guard.Operator != "gte" {
					t.Fatalf("guard operator = %q, want gte", test.input.Guard.Operator)
				}
				return
			}
			apiErr, ok := err.(*apiError)
			if !ok || apiErr.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %s", err, test.wantCode)
			}
		})
	}
}

func TestValidateAccountSettingsDefaultsOmittedRecoveryThreshold(t *testing.T) {
	input := accountSettingsInput{
		ProbeIntervalSeconds:    30,
		ProbeTimeoutSeconds:     7,
		FailureThreshold:        2,
		RateSyncIntervalSeconds: 30,
		SourceType:              "sub2api",
		RechargeRatio:           1,
		GuardOperator:           "gte",
	}
	if err := validateAccountSettings(&input); err != nil {
		t.Fatalf("validateAccountSettings() rejected an omitted recovery threshold: %v", err)
	}
	if input.RecoverySuccessThreshold != nil {
		t.Fatalf("omitted recovery threshold became %#v, want nil so the update preserves the stored value", input.RecoverySuccessThreshold)
	}
}

func TestAccountSchedulingLockKeysAreStable(t *testing.T) {
	accountID := "00000000-0000-4000-8000-000000000001"
	first, second, err := accountSchedulingLockKeys(accountID)
	if err != nil {
		t.Fatal(err)
	}
	againFirst, againSecond, err := accountSchedulingLockKeys(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if first != againFirst || second != againSecond {
		t.Fatalf("lock keys changed: (%d,%d) then (%d,%d)", first, second, againFirst, againSecond)
	}
	if _, _, err := accountSchedulingLockKeys("invalid"); err == nil {
		t.Fatal("invalid account ID did not return an error")
	}
}

func TestProbeControlStateRecoveryThreshold(t *testing.T) {
	tests := []struct {
		name  string
		state probeControlState
		want  bool
	}{
		{name: "not managed", state: probeControlState{RecoverySuccessThreshold: 1}},
		{name: "invalid zero threshold", state: probeControlState{ManagedHold: true}},
		{name: "default first success", state: probeControlState{ManagedHold: true, RecoverySuccessThreshold: 1}, want: true},
		{name: "progress below threshold", state: probeControlState{ManagedHold: true, ConsecutiveRecoverySuccesses: 1, RecoverySuccessThreshold: 3}},
		{name: "next success reaches threshold", state: probeControlState{ManagedHold: true, ConsecutiveRecoverySuccesses: 2, RecoverySuccessThreshold: 3}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.recoveryThresholdReached(); got != test.want {
				t.Fatalf("recoveryThresholdReached() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestValidateBulkAccountRateSync(t *testing.T) {
	valid := bulkAccountSettingsWork{
		SourceType: "newapi", SourceBaseURL: stringPointer("https://example.test"), SourceCredentialSet: true, SourceGroup: stringPointer("default"),
	}
	tests := []struct {
		name     string
		work     bulkAccountSettingsWork
		enabled  bool
		wantCode string
	}{
		{name: "disabled NewAPI does not require configuration", work: bulkAccountSettingsWork{SourceType: "newapi"}},
		{name: "enabled Sub2API does not require NewAPI configuration", work: bulkAccountSettingsWork{SourceType: "sub2api"}, enabled: true},
		{name: "valid NewAPI", work: valid, enabled: true},
		{name: "missing URL", work: bulkAccountSettingsWork{SourceType: "newapi", SourceCredentialSet: true, SourceGroup: stringPointer("default")}, enabled: true, wantCode: "NEWAPI_URL_REQUIRED"},
		{name: "missing group", work: bulkAccountSettingsWork{SourceType: "newapi", SourceBaseURL: stringPointer("https://example.test"), SourceCredentialSet: true}, enabled: true, wantCode: "NEWAPI_GROUP_REQUIRED"},
		{name: "missing credential", work: bulkAccountSettingsWork{SourceType: "newapi", SourceBaseURL: stringPointer("https://example.test"), SourceGroup: stringPointer("default")}, enabled: true, wantCode: "NEWAPI_CREDENTIAL_REQUIRED"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateBulkAccountRateSync(test.work, test.enabled)
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("validateBulkAccountRateSync: %v", err)
				}
				return
			}
			apiErr, ok := err.(*apiError)
			if !ok || apiErr.Code != test.wantCode {
				t.Fatalf("error = %#v, want code %s", err, test.wantCode)
			}
		})
	}
}

func TestProbeControlStateAcceptsOnlyNewResultsFromCurrentSchedulingGeneration(t *testing.T) {
	tests := []struct {
		name  string
		state probeControlState
		token probeControlToken
		want  bool
	}{
		{
			name:  "new result in current generation",
			state: probeControlState{AppliedSequence: 4, SchedulingGeneration: 7},
			token: probeControlToken{Sequence: 5, SchedulingGeneration: 7},
			want:  true,
		},
		{
			name:  "older result completed after newer result",
			state: probeControlState{AppliedSequence: 5, SchedulingGeneration: 7},
			token: probeControlToken{Sequence: 4, SchedulingGeneration: 7},
			want:  false,
		},
		{
			name:  "duplicate result",
			state: probeControlState{AppliedSequence: 5, SchedulingGeneration: 7},
			token: probeControlToken{Sequence: 5, SchedulingGeneration: 7},
			want:  false,
		},
		{
			name:  "result predates manual scheduling intent",
			state: probeControlState{AppliedSequence: 4, SchedulingGeneration: 8},
			token: probeControlToken{Sequence: 5, SchedulingGeneration: 7},
			want:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.state.accepts(test.token); got != test.want {
				t.Fatalf("accepts() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestProbePersistenceContextSurvivesCallerCancellationWithValuesAndDeadline(t *testing.T) {
	type contextKey string
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), contextKey("request"), "value"))
	cancelParent()

	persistenceCtx, cancelPersistence := newProbePersistenceContext(parent)
	defer cancelPersistence()
	if err := persistenceCtx.Err(); err != nil {
		t.Fatalf("persistence context inherited caller cancellation: %v", err)
	}
	if got := persistenceCtx.Value(contextKey("request")); got != "value" {
		t.Fatalf("persistence context value = %v, want value", got)
	}
	deadline, ok := persistenceCtx.Deadline()
	if !ok {
		t.Fatal("persistence context does not have a deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > accountSchedulingOperationTimeout {
		t.Fatalf("persistence context deadline remaining = %v", remaining)
	}
}

func float64Pointer(value float64) *float64 { return &value }

func stringPointer(value string) *string { return &value }
