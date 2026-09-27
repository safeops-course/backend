package config

import (
	"math"
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

// A flag overrides its environment variable, so an invalid value there must not stop the start.
func TestFlagOverridesInvalidBooleanEnv(t *testing.T) {
	invalid := []string{"AUTH_REGISTRATION_ENABLED", "CHAOS_ENABLED"}

	got := envErrorsStillInEffect(invalid, map[string]bool{"registration-enabled": true})
	if len(got) != 1 || got[0] != "CHAOS_ENABLED" {
		t.Fatalf("expected only CHAOS_ENABLED to stay an error, got %v", got)
	}
	if got := envErrorsStillInEffect(invalid, map[string]bool{}); len(got) != 2 {
		t.Fatalf("without flags both errors stay, got %v", got)
	}
}

func TestValidateRejectsNonFiniteOrHugeDelayMax(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), MaxDelaySeconds + 1, 9223372022} {
		cfg := validConfig()
		cfg.DelayMaxSeconds = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("DELAY_MAX_SECONDS=%v must be rejected", value)
		}
	}
	cfg := validConfig()
	cfg.DelayMaxSeconds = MaxDelaySeconds
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DELAY_MAX_SECONDS=%d must be accepted: %v", MaxDelaySeconds, err)
	}
}
