package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		JWTSecret:              strings.Repeat("x", MinJWTSecretLength),
		DelayMaxSeconds:        10,
		LoginAttemptsPerMinute: 10,
		RegistrationsPerMinute: 10,
		PprofAddr:              "127.0.0.1:6060",
	}
}

func TestDefaultsAreTheSafeOnes(t *testing.T) {
	for _, key := range []string{"CHAOS_ENABLED", "PPROF_ENABLED", "PPROF_ADDR", "DELAY_MAX_SECONDS"} {
		t.Setenv(key, "")
	}
	cfg := defaultConfig()
	if cfg.ChaosEnabled {
		t.Errorf("chaos must be off by default")
	}
	if cfg.PprofEnabled {
		t.Errorf("pprof must be off by default")
	}
	if !strings.HasPrefix(cfg.PprofAddr, "127.0.0.1:") {
		t.Errorf("pprof must listen on loopback by default, got %q", cfg.PprofAddr)
	}
	if cfg.DelayMaxSeconds <= 0 || cfg.DelayMaxSeconds > 30 {
		t.Errorf("unexpected default DelayMaxSeconds %v", cfg.DelayMaxSeconds)
	}
}

func TestValidateAcceptsAValidConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	cases := map[string]func(*Config){
		"empty JWT secret":      func(c *Config) { c.JWTSecret = "" },
		"short JWT secret":      func(c *Config) { c.JWTSecret = "change-me" },
		"zero delay max":        func(c *Config) { c.DelayMaxSeconds = 0 },
		"zero login limit":      func(c *Config) { c.LoginAttemptsPerMinute = 0 },
		"zero register limit":   func(c *Config) { c.RegistrationsPerMinute = 0 },
		"pprof without address": func(c *Config) { c.PprofEnabled = true; c.PprofAddr = " " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}
}

// "AUTH_REGISTRATION_ENABLED=no" must stop the start, not silently keep the default (true).
func TestInvalidBooleanEnvStopsTheStart(t *testing.T) {
	t.Setenv("AUTH_REGISTRATION_ENABLED", "no")
	cfg := defaultConfig()
	cfg.JWTSecret = strings.Repeat("x", MinJWTSecretLength)

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "AUTH_REGISTRATION_ENABLED") {
		t.Fatalf("expected an error naming AUTH_REGISTRATION_ENABLED, got %v", err)
	}
}
