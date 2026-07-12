package sub2pool

import "testing"

func TestConfigFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv(envMinimumAccountCount, "")
	t.Setenv(envMaximumChanges, "")
	t.Setenv(envLowBalanceThreshold, "")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if cfg.MinimumAccountCount != 20 || cfg.MaximumChanges != 20 || cfg.LowBalanceThreshold != 10 {
		t.Fatalf("defaults = %#v", cfg)
	}

	t.Setenv(envMinimumAccountCount, "30")
	t.Setenv(envMaximumChanges, "12")
	t.Setenv(envLowBalanceThreshold, "8.5")
	cfg, err = ConfigFromEnv()
	if err != nil {
		t.Fatalf("overrides: %v", err)
	}
	if cfg.MinimumAccountCount != 30 || cfg.MaximumChanges != 12 || cfg.LowBalanceThreshold != 8.5 {
		t.Fatalf("overrides = %#v", cfg)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	t.Setenv(envMinimumAccountCount, "0")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid minimum account count was accepted")
	}
}
