package config

import "testing"

func TestLogDirectoryConfiguration(t *testing.T) {
	t.Setenv("S2AM_DATABASE_URL", "postgres://localhost/s2am")
	t.Setenv("S2AM_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("S2AM_LOG_DIR", "")
	defaultConfig, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if defaultConfig.LogDir != "./logs" {
		t.Fatalf("default log directory = %q", defaultConfig.LogDir)
	}

	t.Setenv("S2AM_LOG_DIR", "  /var/lib/s2am-go/logs  ")
	configured, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if configured.LogDir != "/var/lib/s2am-go/logs" {
		t.Fatalf("configured log directory = %q", configured.LogDir)
	}
}
