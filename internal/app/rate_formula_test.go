package app

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestCalculateManagedRateModes(t *testing.T) {
	tests := []struct {
		mode       string
		offset     float64
		expression string
		want       float64
	}{
		{mode: "first", offset: 0.1, want: 0.7},
		{mode: "average", want: 0.8},
		{mode: "min", want: 0.6},
		{mode: "max", offset: -0.1, want: 0.9},
		{mode: "custom", expression: "round((r0 * 0.7) + (rate(1) * 0.3), 4)", want: 0.72},
		{mode: "custom", expression: "clamp(max() + current, 0, 2) / count", want: 0.9},
	}
	for _, test := range tests {
		got, err := calculateManagedRate(test.mode, test.offset, test.expression, 0.8, []float64{0.6, 1.0, math.NaN()})
		if err != nil {
			t.Fatalf("%s: %v", test.mode, err)
		}
		if math.Abs(got-test.want) > 1e-9 {
			t.Fatalf("%s: got %v want %v", test.mode, got, test.want)
		}
	}
}

func TestCalculateManagedRateRejectsUnsafeOrInvalidExpressions(t *testing.T) {
	inputs := []string{
		"count >= 2",
		"process.exit(1)",
		"1 / 0",
		"unknown + 1",
		"round(1, 20)",
		"clamp(1, 2, 0)",
	}
	for _, input := range inputs {
		if _, err := calculateManagedRate("custom", 0, input, 1, []float64{1}); err == nil {
			t.Fatalf("expected %q to fail", input)
		}
	}
}

func TestCalculateManagedRateValidation(t *testing.T) {
	if _, err := calculateManagedRate("average", 0, "", 1, nil); err == nil || !strings.Contains(err.Error(), "没有可用") {
		t.Fatalf("unexpected empty rate error: %v", err)
	}
	if _, err := calculateManagedRate("custom", 0, "-1", 1, []float64{1}); err == nil {
		t.Fatal("expected negative result to fail")
	}
	if got, err := calculateManagedRate("custom", 0, "avg * 1.08", 1, []float64{0.33333, 0.55555}); err != nil || got != 0.48 {
		t.Fatalf("unexpected rounded result %v, %v", got, err)
	}
}

func TestValidateRateExpressionDoesNotEvaluatePlaceholderValues(t *testing.T) {
	valid := []string{
		"1 / (r0 - 1)",
		"clamp(current, r0, r1)",
		"round(avg, count)",
	}
	for _, expression := range valid {
		if err := validateRateExpression(expression); err != nil {
			t.Fatalf("valid expression %q was rejected: %v", expression, err)
		}
	}

	invalid := []string{
		"unknown + 1",
		"min(, 1)",
		"abs(1, 2)",
		"current trailing",
	}
	for _, expression := range invalid {
		if err := validateRateExpression(expression); err == nil {
			t.Fatalf("invalid expression %q was accepted", expression)
		}
	}
}

func TestValidateGroupRateConfigRequiresBindingWhenEnabled(t *testing.T) {
	input := groupRateConfigInput{Enabled: true, Mode: "average"}
	err := validateGroupRateConfig(&input)
	var apiErr *apiError
	if err == nil || !errors.As(err, &apiErr) || apiErr.Code != "RATE_BINDING_REQUIRED" {
		t.Fatalf("unexpected validation result: %v", err)
	}
}

func TestValidateGroupRateConfigOnlyChecksFormulaStructure(t *testing.T) {
	expression := "1 / (r0 - 1)"
	input := groupRateConfigInput{
		Enabled:    true,
		Mode:       "custom",
		Expression: &expression,
		Bindings:   []string{"source-account"},
	}
	if err := validateGroupRateConfig(&input); err != nil {
		t.Fatalf("structurally valid formula was rejected using placeholder values: %v", err)
	}
}
