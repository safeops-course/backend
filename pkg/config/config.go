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
	Port               int
	UIMessage          string
	UIColor            string
	Version            string
	Commit             string
	CommitShort        string
	BuildDate          string
	RandomDelayMax     int
	RandomErrorRate    float64
	ConfigPath         string // Directory to watch for config changes (ConfigMaps/Secrets)
	JWTSecret          string // Secret for signing JWT tokens
	JWTTokenTTLMinutes int    // Token TTL in minutes
	DatabaseURL        string // Postgres DSN used for auth/app data
	AuthDBPath         string // Fallback file path for local auth store
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
	configPath := flag.String("config-path", defaults.ConfigPath, "Directory to watch for config changes (ConfigMaps/Secrets)")
	jwtSecret := flag.String("jwt-secret", defaults.JWTSecret, "Secret for signing JWT tokens")
	jwtTokenTTLMinutes := flag.Int("jwt-token-ttl-minutes", defaults.JWTTokenTTLMinutes, "JWT token TTL in minutes")
	databaseURL := flag.String("database-url", defaults.DatabaseURL, "Postgres connection string used for auth store")
	authDBPath := flag.String("auth-db-path", defaults.AuthDBPath, "Path to local fallback auth user store JSON file")

	flag.Parse()

	cfg := Config{
		Port:               *port,
		UIMessage:          *message,
		UIColor:            *color,
		Version:            *version,
		Commit:             *commit,
		CommitShort:        *commitShort,
		BuildDate:          *buildDate,
		RandomDelayMax:     *randomDelay,
		RandomErrorRate:    *randomError,
		ConfigPath:         *configPath,
		JWTSecret:          *jwtSecret,
		JWTTokenTTLMinutes: *jwtTokenTTLMinutes,
		DatabaseURL:        *databaseURL,
		AuthDBPath:         *authDBPath,
	}

	return cfg
}

func defaultConfig() Config {
	return Config{
		Port:               envInt("PORT", 8080),
		UIMessage:          envString("UI_MESSAGE", "Welcome to the SRE control plane"),
		UIColor:            envString("UI_COLOR", "#2E5CFF"),
		Version:            envString("APP_VERSION", version.Version),
		Commit:             envString("APP_COMMIT", version.Commit),
		CommitShort:        envString("APP_COMMIT_SHORT", version.ShortCommit),
		BuildDate:          envString("APP_BUILD_DATE", version.BuildDate),
		RandomDelayMax:     envInt("RANDOM_DELAY_MAX", 0),
		RandomErrorRate:    envFloat("RANDOM_ERROR_RATE", 0),
		ConfigPath:         envString("CONFIG_PATH", ""),
		JWTSecret:          envString("JWT_SECRET", ""),
		JWTTokenTTLMinutes: envInt("JWT_TOKEN_TTL_MINUTES", 60),
		DatabaseURL:        envString("DATABASE_URL", ""),
		AuthDBPath:         envString("AUTH_DB_PATH", "data/users.json"),
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
