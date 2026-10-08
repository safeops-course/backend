package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ldbl/sre/backend/pkg/migrations"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

// Connection pool share of one replica (see limitPool).
const (
	maxOpenConns = 10
	maxIdleConns = 5
)

var (
	errUserExists         = errors.New("user already exists")
	errInvalidCredentials = errors.New("invalid username or password")
	// errInvalidInput marks a request the client must fix (400), not a server fault.
	errInvalidInput = errors.New("invalid input")
)

type authUserStore interface {
	createUser(ctx context.Context, username, password, displayName string) (userRecord, error)
	authenticate(ctx context.Context, username, password string) (userRecord, error)
	Close() error
}

type userRecord struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	PasswordSalt string    `json:"password_salt"`
	CreatedAt    time.Time `json:"created_at"`
	DisplayName  string    `json:"display_name,omitempty"` // optional; empty when the user set none (schema 2)
}

type userStoreState struct {
	NextID int64        `json:"next_id"`
	Users  []userRecord `json:"users"`
}

type fileUserStore struct {
	mu     sync.RWMutex
	path   string
	nextID int64
	users  map[string]userRecord
}

func newFileUserStore(path string) (*fileUserStore, error) {
	if strings.TrimSpace(path) == "" {
		path = "data/users.json"
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create user store dir: %w", err)
	}

	s := &fileUserStore{
		path:   path,
		nextID: 1,
		users:  make(map[string]userRecord),
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *fileUserStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read user store: %w", err)
	}

	var state userStoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse user store: %w", err)
	}

	if state.NextID > 0 {
		s.nextID = state.NextID
	}

	for _, u := range state.Users {
		s.users[strings.ToLower(u.Username)] = u
		if u.ID >= s.nextID {
			s.nextID = u.ID + 1
		}
	}

	return nil
}

func (s *fileUserStore) saveLocked() error {
	state := userStoreState{
		NextID: s.nextID,
		Users:  make([]userRecord, 0, len(s.users)),
	}
	for _, u := range s.users {
		state.Users = append(state.Users, u)
	}

	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal user store: %w", err)
	}

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, payload, 0o600); err != nil {
		return fmt.Errorf("write user store temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("replace user store: %w", err)
	}

	return nil
}

func (s *fileUserStore) createUser(_ context.Context, username, password, displayName string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, err
	}
	normalizedDisplayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return userRecord{}, err
	}
	if err := validatePassword(password); err != nil {
		return userRecord{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lookupKey := strings.ToLower(normalizedUsername)
	if _, exists := s.users[lookupKey]; exists {
		return userRecord{}, errUserExists
	}

	hashedPassword, err := hashPassword(password)
	if err != nil {
		return userRecord{}, fmt.Errorf("hash password: %w", err)
	}

	record := userRecord{
		ID:           s.nextID,
		Username:     normalizedUsername,
		PasswordSalt: "",
		PasswordHash: hashedPassword,
		CreatedAt:    time.Now().UTC(),
		DisplayName:  normalizedDisplayName,
	}
	s.nextID++
	s.users[lookupKey] = record

	if err := s.saveLocked(); err != nil {
		delete(s.users, lookupKey)
		s.nextID--
		return userRecord{}, err
	}

	return record, nil
}

func (s *fileUserStore) authenticate(_ context.Context, username, password string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, errInvalidCredentials
	}

	s.mu.RLock()
	record, exists := s.users[strings.ToLower(normalizedUsername)]
	s.mu.RUnlock()
	if !exists {
		return userRecord{}, errInvalidCredentials
	}

	if err := comparePassword(record.PasswordHash, password); err != nil {
		return userRecord{}, errInvalidCredentials
	}

	return record, nil
}

func (s *fileUserStore) Close() error {
	return nil
}

type postgresUserStore struct {
	db *sql.DB
}

func newPostgresUserStore(databaseURL string) (*postgresUserStore, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is empty")
	}

	pgxCfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	pgxCfg.Tracer = otelpgx.NewTracer()

	db := stdlib.OpenDB(*pgxCfg)
	limitPool(db)

	store := &postgresUserStore{db: db}

	// Retry connecting to postgres with backoff.
	// Backend often starts before postgres is ready in Kubernetes.
	const maxRetries = 5
	var lastErr error
	for i := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lastErr = db.PingContext(ctx)
		cancel()
		if lastErr == nil {
			break
		}
		if i < maxRetries-1 {
			time.Sleep(time.Duration(i+1) * time.Second)
		}
	}
	if lastErr != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping auth database after %d attempts: %w", maxRetries, lastErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

// ensureSchema refuses to run on a schema this build cannot use. The schema itself is created and
// changed only by versioned migrations (pkg/migrations), applied by `backend migrate` - the migrate
// initContainer in Kubernetes - never by the app at startup.
func (s *postgresUserStore) ensureSchema(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := migrations.Check(ctx, s.db); err != nil {
		return fmt.Errorf("database schema: %w", err)
	}
	return nil
}

func (s *postgresUserStore) createUser(ctx context.Context, username, password, displayName string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, err
	}
	normalizedDisplayName, err := normalizeDisplayName(displayName)
	if err != nil {
		return userRecord{}, err
	}
	if err := validatePassword(password); err != nil {
		return userRecord{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	hashedPassword, err := hashPassword(password)
	if err != nil {
		return userRecord{}, fmt.Errorf("hash password: %w", err)
	}

	const q = `
INSERT INTO app_users (username, password_hash, password_salt, display_name)
VALUES ($1, $2, $3, NULLIF($4, ''))
ON CONFLICT ((lower(username))) DO NOTHING
RETURNING id, username, password_hash, password_salt, created_at, COALESCE(display_name, '');
`

	record := userRecord{}
	err = s.db.QueryRowContext(ctx, q, normalizedUsername, hashedPassword, "", normalizedDisplayName).
		Scan(&record.ID, &record.Username, &record.PasswordHash, &record.PasswordSalt, &record.CreatedAt, &record.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return userRecord{}, errUserExists
	}
	if err != nil {
		return userRecord{}, fmt.Errorf("insert user: %w", err)
	}

	return record, nil
}

func (s *postgresUserStore) authenticate(ctx context.Context, username, password string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, errInvalidCredentials
	}
	if ctx == nil {
		ctx = context.Background()
	}

	const q = `
SELECT id, username, password_hash, password_salt, created_at, COALESCE(display_name, '')
FROM app_users
WHERE lower(username) = lower($1)
LIMIT 1;
`

	record := userRecord{}
	err = s.db.QueryRowContext(ctx, q, normalizedUsername).
		Scan(&record.ID, &record.Username, &record.PasswordHash, &record.PasswordSalt, &record.CreatedAt, &record.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return userRecord{}, errInvalidCredentials
	}
	if err != nil {
		return userRecord{}, fmt.Errorf("query user: %w", err)
	}

	if err := comparePassword(record.PasswordHash, password); err != nil {
		return userRecord{}, errInvalidCredentials
	}

	return record, nil
}

func (s *postgresUserStore) Close() error {
	return s.db.Close()
}

// maxDisplayNameRunes matches the display_name column (VARCHAR(64), characters, not bytes).
const maxDisplayNameRunes = 64

// normalizeDisplayName trims the optional display name; empty means none. Longer than the column is
// the client's error (400), not a truncation.
func normalizeDisplayName(displayName string) (string, error) {
	trimmed := strings.TrimSpace(displayName)
	if utf8.RuneCountInString(trimmed) > maxDisplayNameRunes {
		return "", fmt.Errorf("%w: display_name must be at most %d characters", errInvalidInput, maxDisplayNameRunes)
	}
	return trimmed, nil
}

func normalizeUsername(username string) (string, error) {
	trimmed := strings.TrimSpace(username)
	if len(trimmed) < 3 {
		return "", fmt.Errorf("%w: username must be at least 3 characters", errInvalidInput)
	}
	if len(trimmed) > 64 {
		return "", fmt.Errorf("%w: username must be at most 64 characters", errInvalidInput)
	}
	return trimmed, nil
}

// bcrypt uses at most 72 bytes of a password and refuses longer ones.
const maxPasswordBytes = 72

func validatePassword(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("%w: password must be at least 8 characters", errInvalidInput)
	}
	if len(password) > maxPasswordBytes {
		return fmt.Errorf("%w: password must be at most %d bytes", errInvalidInput, maxPasswordBytes)
	}
	return nil
}

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// comparePassword refuses a password over maxPasswordBytes before bcrypt sees it:
// CompareHashAndPassword uses only the first 72 bytes, so "the 72-byte password
// plus anything" would match. Both stores call this, so both are covered.
func comparePassword(storedHash, password string) error {
	if len(password) > maxPasswordBytes {
		return errInvalidCredentials
	}
	return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password))
}

// limitPool caps the pool. database/sql opens connections without limit by
// default, while every replica shares Postgres' max_connections (100 by default
// in CloudNativePG): each pod gets a fixed share - production runs up to 3
// replicas (HPA), so 30 at most.
func limitPool(db *sql.DB) {
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}
