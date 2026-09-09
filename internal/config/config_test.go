package config

import "testing"

func TestLogDirectoryConfiguration(t *testing.T) {
	t.Setenv("PILOT_DATABASE_URL", "postgres://localhost/pilot")
	t.Setenv("PILOT_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=") // gitleaks:allow -- deterministic fixture
	t.Setenv("PILOT_LOG_DIR", "")
	defaultConfig, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.LogDir != "./logs" {
		t.Fatalf("default log directory = %q", defaultConfig.LogDir)
	}

	t.Setenv("PILOT_LOG_DIR", "  /var/lib/upstream-pilot/logs  ")
	configured, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configured.LogDir != "/var/lib/upstream-pilot/logs" {
		t.Fatalf("configured log directory = %q", configured.LogDir)
	}
}

func TestOperationsExtensionDefaultsAndSwitches(t *testing.T) {
	t.Setenv("PILOT_DATABASE_URL", "postgres://localhost/pilot")
	t.Setenv("PILOT_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OperationsExtensionsEnabled || !cfg.BillingAuditEnabled || cfg.RedemptionEnabled || !cfg.RiskAnalysisEnabled || cfg.AbuseHubEnabled {
		t.Fatalf("unexpected extension defaults: %+v", cfg)
	}
	t.Setenv("PILOT_REDEMPTION_ENABLED", "true")
	t.Setenv("PILOT_ABUSEHUB_ENABLED", "true")
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RedemptionEnabled || !cfg.AbuseHubEnabled {
		t.Fatalf("switches not applied: %+v", cfg)
	}
}
