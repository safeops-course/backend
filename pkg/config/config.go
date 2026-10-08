package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

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

	// Security switches. Every default is the safe one: a deployment has to opt in explicitly.
	ChaosEnabled           bool    // /panic and readyz|livez enable|disable exist only when true
	PprofEnabled           bool    // Go profiling on a separate listener (PprofAddr), never on the public router
	PprofAddr              string  // Listen address of the profiling server; loopback by default
	DelayMaxSeconds        float64 // Upper bound for /delay/{seconds}
	RegistrationEnabled    bool    // POST /auth/register creates users only when true
	LoginAttemptsPerMinute int     // Per username, per pod
	RegistrationsPerMinute int     // For the whole pod

	// Feature flags (FEATURE_*, public in /env). Off by default; an environment overlay switches them.
	FeatureDisplayName bool // auth responses carry the user's display_name (schema version 2, Chapter 18)

	invalidEnv []string // boolean env vars with a value strconv.ParseBool rejects (reported by Validate)
}

// MinJWTSecretLength is the shortest accepted JWT_SECRET (bytes). HS256 is only as strong as its key.
const MinJWTSecretLength = 32

// MaxDelaySeconds bounds DELAY_MAX_SECONDS; the HTTP WriteTimeout is derived from it (+15s).
const MaxDelaySeconds = 300

// Validate fails loudly on a configuration that would start an insecure or broken server.
func (c Config) Validate() error {
	var problems []string
	for _, key := range c.invalidEnv {
		problems = append(problems, key+" must be true or false")
	}
	if len(strings.TrimSpace(c.JWTSecret)) < MinJWTSecretLength {
		problems = append(problems, fmt.Sprintf("JWT_SECRET must be at least %d characters", MinJWTSecretLength))
	}
	// NaN fails every comparison, so "!(x > 0)" rejects it together with zero and negatives.
	if !(c.DelayMaxSeconds > 0) || c.DelayMaxSeconds > MaxDelaySeconds {
		problems = append(problems, fmt.Sprintf("DELAY_MAX_SECONDS must be greater than 0 and at most %d", MaxDelaySeconds))
	}
	if c.LoginAttemptsPerMinute < 1 {
		problems = append(problems, "AUTH_LOGIN_ATTEMPTS_PER_MINUTE must be at least 1")
	}
	if c.RegistrationsPerMinute < 1 {
		problems = append(problems, "AUTH_REGISTRATIONS_PER_MINUTE must be at least 1")
	}
	if c.PprofEnabled && strings.TrimSpace(c.PprofAddr) == "" {
		problems = append(problems, "PPROF_ADDR must be set when PPROF_ENABLED=true")
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Parse reads configuration from environment variables and command-line flags.
func Parse() Config {
	return parse(flag.CommandLine, os.Args[1:])
}

// parse is Parse on a given flag set and arguments, so a test can run it more than once. Every
// Config field must be copied into the result below: a field set only in defaultConfig is silently
// dropped here - TestParseKeepsEveryEnvironmentSetting fails when one is.
func parse(fs *flag.FlagSet, args []string) Config {
	defaults := defaultConfig()

	port := fs.Int("port", defaults.Port, "HTTP listen port")
	message := fs.String("message", defaults.UIMessage, "UI message rendered on root page")
	color := fs.String("color", defaults.UIColor, "UI accent color in hex format")
	version := fs.String("version", defaults.Version, "Application version")
	commit := fs.String("commit", defaults.Commit, "Git commit hash")
	commitShort := fs.String("commit-short", defaults.CommitShort, "Short git commit hash")
	buildDate := fs.String("build-date", defaults.BuildDate, "Build timestamp in RFC3339 format")
	randomDelay := fs.Int("random-delay", defaults.RandomDelayMax, "Maximum random delay in milliseconds injected per request")
	randomError := fs.Float64("random-error-rate", defaults.RandomErrorRate, "Probability [0-1] to inject random HTTP 500 errors")
	configPath := fs.String("config-path", defaults.ConfigPath, "Directory to watch for config changes (ConfigMaps/Secrets)")
	jwtSecret := fs.String("jwt-secret", defaults.JWTSecret, "Secret for signing JWT tokens")
	jwtTokenTTLMinutes := fs.Int("jwt-token-ttl-minutes", defaults.JWTTokenTTLMinutes, "JWT token TTL in minutes")
	databaseURL := fs.String("database-url", defaults.DatabaseURL, "Postgres connection string used for auth store")
	authDBPath := fs.String("auth-db-path", defaults.AuthDBPath, "Path to local fallback auth user store JSON file")
	chaosEnabled := fs.Bool("chaos-enabled", defaults.ChaosEnabled, "Expose /panic and readiness/liveness toggles (authenticated)")
	pprofEnabled := fs.Bool("pprof-enabled", defaults.PprofEnabled, "Serve Go profiling on pprof-addr")
	pprofAddr := fs.String("pprof-addr", defaults.PprofAddr, "Listen address of the profiling server")
	delayMaxSeconds := fs.Float64("delay-max-seconds", defaults.DelayMaxSeconds, "Upper bound for /delay/{seconds}")
	registrationEnabled := fs.Bool("registration-enabled", defaults.RegistrationEnabled, "Allow POST /auth/register")
	loginAttemptsPerMinute := fs.Int("login-attempts-per-minute", defaults.LoginAttemptsPerMinute, "Login attempts per username per minute (per pod)")
	registrationsPerMinute := fs.Int("registrations-per-minute", defaults.RegistrationsPerMinute, "Registrations per minute (per pod)")
	featureDisplayName := fs.Bool("feature-display-name", defaults.FeatureDisplayName, "Return the stored display_name in register/login responses")

	// flag.CommandLine exits on a bad flag by itself (ExitOnError); a test's set gets no bad flags.
	_ = fs.Parse(args)

	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })
	invalidEnv := envErrorsStillInEffect(defaults.invalidEnv, setFlags)

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

		ChaosEnabled:           *chaosEnabled,
		PprofEnabled:           *pprofEnabled,
		PprofAddr:              *pprofAddr,
		DelayMaxSeconds:        *delayMaxSeconds,
		RegistrationEnabled:    *registrationEnabled,
		LoginAttemptsPerMinute: *loginAttemptsPerMinute,
		RegistrationsPerMinute: *registrationsPerMinute,

		FeatureDisplayName: *featureDisplayName,

		invalidEnv: invalidEnv,
	}

	return cfg
}

func defaultConfig() Config {
	var invalidEnv []string
	cfg := Config{
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
		DatabaseURL:        buildDatabaseURL(),
		AuthDBPath:         envString("AUTH_DB_PATH", "/tmp/users.json"),

		ChaosEnabled:           envBool("CHAOS_ENABLED", false, &invalidEnv),
		PprofEnabled:           envBool("PPROF_ENABLED", false, &invalidEnv),
		PprofAddr:              envString("PPROF_ADDR", "127.0.0.1:6060"),
		DelayMaxSeconds:        envFloat("DELAY_MAX_SECONDS", 10),
		RegistrationEnabled:    envBool("AUTH_REGISTRATION_ENABLED", true, &invalidEnv),
		LoginAttemptsPerMinute: envInt("AUTH_LOGIN_ATTEMPTS_PER_MINUTE", 10),
		RegistrationsPerMinute: envInt("AUTH_REGISTRATIONS_PER_MINUTE", 10),

		FeatureDisplayName: envBool("FEATURE_DISPLAY_NAME", false, &invalidEnv),
	}
	cfg.invalidEnv = invalidEnv
	return cfg
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

// flagForEnv maps each boolean environment variable to the flag that overrides it.
var flagForEnv = map[string]string{
	"CHAOS_ENABLED":             "chaos-enabled",
	"PPROF_ENABLED":             "pprof-enabled",
	"AUTH_REGISTRATION_ENABLED": "registration-enabled",
	"FEATURE_DISPLAY_NAME":      "feature-display-name",
}

// envErrorsStillInEffect drops the invalid environment variables whose flag was given on the
// command line: the flag overrides the variable, so its bad value no longer matters.
func envErrorsStillInEffect(invalidEnv []string, setFlags map[string]bool) []string {
	var inEffect []string
	for _, key := range invalidEnv {
		if flagName, hasFlag := flagForEnv[key]; hasFlag && setFlags[flagName] {
			continue
		}
		inEffect = append(inEffect, key)
	}
	return inEffect
}

// envBool accepts the strconv.ParseBool spellings (true/false, 1/0, ...). Any other value is recorded
// in invalid so Validate stops the start: "CHAOS_ENABLED=yes" must not silently mean the default.
func envBool(key string, fallback bool, invalid *[]string) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		*invalid = append(*invalid, key)
		return fallback
	}
	return parsed
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

// buildDatabaseURL returns DATABASE_URL if set, otherwise composes it from
// POSTGRES_USER, POSTGRES_PASSWORD, POSTGRES_HOST, POSTGRES_PORT, POSTGRES_DB.
// Composing from parts ensures the password is properly URL-encoded.
// DatabaseURLFromEnv is the Postgres DSN the app would use (DATABASE_URL or POSTGRES_*), for
// commands that need only the database - `backend migrate`.
func DatabaseURLFromEnv() string {
	return buildDatabaseURL()
}

func buildDatabaseURL() string {
	if v := envString("DATABASE_URL", ""); v != "" {
		return v
	}
	user := envString("POSTGRES_USER", "")
	pass := envString("POSTGRES_PASSWORD", "")
	host := envString("POSTGRES_HOST", "")
	if user == "" || host == "" {
		return ""
	}
	port := envString("POSTGRES_PORT", "5432")
	dbName := envString("POSTGRES_DB", "app")
	sslMode := envString("POSTGRES_SSLMODE", "disable")

	u := &url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(user, pass),
		Host:     fmt.Sprintf("%s:%s", host, port),
		Path:     dbName,
		RawQuery: fmt.Sprintf("sslmode=%s", sslMode),
	}
	return u.String()
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.Port)
}
