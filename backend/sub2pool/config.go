package sub2pool

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envMinimumAccountCount = "SUB2_POOL_MIN_ACCOUNT_COUNT"
	envMaximumChanges      = "SUB2_POOL_MAX_CHANGES"
	envLowBalanceThreshold = "SUB2_POOL_LOW_BALANCE_THRESHOLD"
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
