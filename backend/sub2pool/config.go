package sub2pool

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envMinimumAccountCount          = "SUB2_POOL_MIN_ACCOUNT_COUNT"
	envMaximumChanges               = "SUB2_POOL_MAX_CHANGES"
	envLowBalanceThreshold          = "SUB2_POOL_LOW_BALANCE_THRESHOLD"
	envAccountRateMapImportPath     = "SUB2_POOL_ACCOUNT_RATE_MAP_IMPORT_PATH"
	envAccountRateMapImportTargetID = "SUB2_POOL_ACCOUNT_RATE_MAP_IMPORT_TARGET_ID"
)

func ConfigFromEnv() (Config, error) {
	cfg := Config{}.withDefaults()
	var err error
	if cfg.MinimumAccountCount, err = intEnv(envMinimumAccountCount, cfg.MinimumAccountCount); err != nil {
		return Config{}, err
	}
	if cfg.MaximumChanges, err = intEnv(envMaximumChanges, cfg.MaximumChanges); err != nil {
		return Config{}, err
	}
	if cfg.LowBalanceThreshold, err = floatEnv(envLowBalanceThreshold, cfg.LowBalanceThreshold); err != nil {
		return Config{}, err
	}
	cfg.AccountRateMapImportPath = strings.TrimSpace(os.Getenv(envAccountRateMapImportPath))
	if cfg.AccountRateMapImportTargetID, err = optionalPositiveIntEnv(envAccountRateMapImportTargetID); err != nil {
		return Config{}, err
	}
	if (cfg.AccountRateMapImportPath == "") != (cfg.AccountRateMapImportTargetID == 0) {
		return Config{}, fmt.Errorf(
			"%s and %s must be set together",
			envAccountRateMapImportPath,
			envAccountRateMapImportTargetID,
		)
	}
	return cfg, nil
}

func intEnv(name string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func floatEnv(name string, fallback float64) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive number", name)
	}
	return value, nil
}

func optionalPositiveIntEnv(name string) (uint, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return uint(value), nil
}
