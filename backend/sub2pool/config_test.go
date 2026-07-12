package sub2pool

import "testing"

func TestConfigFromEnvDefaultsAndOverrides(t *testing.T) {
	t.Setenv(envMinimumAccountCount, "")
	t.Setenv(envMaximumChanges, "")
	t.Setenv(envLowBalanceThreshold, "")
	t.Setenv(envAccountRateMapImportPath, "")
	t.Setenv(envAccountRateMapImportTargetID, "")
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
	t.Setenv(envAccountRateMapImportPath, "")
	t.Setenv(envAccountRateMapImportTargetID, "")
	t.Setenv(envMinimumAccountCount, "0")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("invalid minimum account count was accepted")
	}
}

func TestConfigFromEnvRequiresCompleteAccountRateMapImport(t *testing.T) {
	t.Setenv(envMinimumAccountCount, "")
	t.Setenv(envMaximumChanges, "")
	t.Setenv(envLowBalanceThreshold, "")
	t.Setenv(envAccountRateMapImportPath, "/app/data/legacy-map.json")
	t.Setenv(envAccountRateMapImportTargetID, "")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("map import path without target id was accepted")
	}

	t.Setenv(envAccountRateMapImportPath, "")
	t.Setenv(envAccountRateMapImportTargetID, "1")
	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("map import target id without path was accepted")
	}

	t.Setenv(envAccountRateMapImportPath, "/app/data/legacy-map.json")
	t.Setenv(envAccountRateMapImportTargetID, "1")
	if _, err := ConfigFromEnv(); err != nil {
		t.Fatalf("complete map import config was rejected: %v", err)
	}
}
