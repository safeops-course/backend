package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/ldbl/sre/backend/pkg/version"
)

// Config holds runtime configuration for the backend service.
type Config struct {
	Port            int
	UIMessage       string
	UIColor         string
	Version         string
	Commit          string
	CommitShort     string
	BuildDate       string
	RandomDelayMax  int
	RandomErrorRate float64
}

// Parse reads configuration from environment variables and command-line flags.
func Parse() Config {
	defaults := defaultConfig()

	port := flag.Int("port", defaults.Port, "HTTP listen port")
	message := flag.String("message", defaults.UIMessage, "UI message rendered on root page")
	color := flag.String("color", defaults.UIColor, "UI accent color in hex format")
	version := flag.String("version", defaults.Version, "Application version")
	commit := flag.String("commit", defaults.Commit, "Git commit hash")
	commitShort := flag.String("commit-short", defaults.CommitShort, "Short git commit hash")
	buildDate := flag.String("build-date", defaults.BuildDate, "Build timestamp in RFC3339 format")
	randomDelay := flag.Int("random-delay", defaults.RandomDelayMax, "Maximum random delay in milliseconds injected per request")
	randomError := flag.Float64("random-error-rate", defaults.RandomErrorRate, "Probability [0-1] to inject random HTTP 500 errors")

	flag.Parse()

	cfg := Config{
		Port:            *port,
		UIMessage:       *message,
		UIColor:         *color,
		Version:         *version,
		Commit:          *commit,
		CommitShort:     *commitShort,
		BuildDate:       *buildDate,
		RandomDelayMax:  *randomDelay,
		RandomErrorRate: *randomError,
	}

	return cfg
}

func defaultConfig() Config {
	return Config{
		Port:            envInt("PORT", 8080),
		UIMessage:       envString("UI_MESSAGE", "Welcome to the SRE control plane"),
		UIColor:         envString("UI_COLOR", "#2E5CFF"),
		Version:         envString("APP_VERSION", version.Version),
		Commit:          envString("APP_COMMIT", version.Commit),
		CommitShort:     envString("APP_COMMIT_SHORT", version.ShortCommit),
		BuildDate:       envString("APP_BUILD_DATE", version.BuildDate),
		RandomDelayMax:  envInt("RANDOM_DELAY_MAX", 0),
		RandomErrorRate: envFloat("RANDOM_ERROR_RATE", 0),
	}
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		parsed, err := strconv.Atoi(v)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v, ok := os.LookupEnv(key); ok {
		parsed, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
