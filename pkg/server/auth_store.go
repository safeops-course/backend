package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	errUserExists         = errors.New("user already exists")
	errInvalidCredentials = errors.New("invalid username or password")
)

type authUserStore interface {
	createUser(ctx context.Context, username, password string) (userRecord, error)
	authenticate(ctx context.Context, username, password string) (userRecord, error)
}

type userRecord struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"password_hash"`
	PasswordSalt string    `json:"password_salt"`
	CreatedAt    time.Time `json:"created_at"`
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
	if err := os.MkdirAll(dir, 0o755); err != nil {
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

func (s *fileUserStore) createUser(_ context.Context, username, password string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, err
	}
	if len(password) < 8 {
		return userRecord{}, errors.New("password must be at least 8 characters")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lookupKey := strings.ToLower(normalizedUsername)
	if _, exists := s.users[lookupKey]; exists {
		return userRecord{}, errUserExists
	}

	salt, err := randomSalt()
	if err != nil {
		return userRecord{}, fmt.Errorf("generate password salt: %w", err)
	}

	record := userRecord{
		ID:           s.nextID,
		Username:     normalizedUsername,
		PasswordSalt: salt,
		PasswordHash: hashPassword(password, salt),
		CreatedAt:    time.Now().UTC(),
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

	if hashPassword(password, record.PasswordSalt) != record.PasswordHash {
		return userRecord{}, errInvalidCredentials
	}

	return record, nil
}

type postgresUserStore struct {
	db *sql.DB
}

func newPostgresUserStore(databaseURL string) (*postgresUserStore, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, errors.New("database URL is empty")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open auth database: %w", err)
	}

	store := &postgresUserStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping auth database: %w", err)
	}

	if err := store.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *postgresUserStore) ensureSchema(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	const createTableStmt = `
CREATE TABLE IF NOT EXISTS app_users (
	id BIGSERIAL PRIMARY KEY,
	username VARCHAR(64) NOT NULL,
	password_hash TEXT NOT NULL,
	password_salt TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`

	if _, err := s.db.ExecContext(ctx, createTableStmt); err != nil {
		return fmt.Errorf("initialize auth table: %w", err)
	}

	const createIndexStmt = `
CREATE UNIQUE INDEX IF NOT EXISTS app_users_username_lower_uq ON app_users ((lower(username)));
`

	if _, err := s.db.ExecContext(ctx, createIndexStmt); err != nil {
		return fmt.Errorf("initialize auth index: %w", err)
	}

	return nil
}

func (s *postgresUserStore) createUser(ctx context.Context, username, password string) (userRecord, error) {
	normalizedUsername, err := normalizeUsername(username)
	if err != nil {
		return userRecord{}, err
	}
	if len(password) < 8 {
		return userRecord{}, errors.New("password must be at least 8 characters")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	salt, err := randomSalt()
	if err != nil {
		return userRecord{}, fmt.Errorf("generate password salt: %w", err)
	}

	const q = `
INSERT INTO app_users (username, password_hash, password_salt)
VALUES ($1, $2, $3)
ON CONFLICT ((lower(username))) DO NOTHING
RETURNING id, username, password_hash, password_salt, created_at;
`

	record := userRecord{}
	err = s.db.QueryRowContext(ctx, q, normalizedUsername, hashPassword(password, salt), salt).
		Scan(&record.ID, &record.Username, &record.PasswordHash, &record.PasswordSalt, &record.CreatedAt)
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
SELECT id, username, password_hash, password_salt, created_at
FROM app_users
WHERE lower(username) = lower($1)
LIMIT 1;
`

	record := userRecord{}
	err = s.db.QueryRowContext(ctx, q, normalizedUsername).
		Scan(&record.ID, &record.Username, &record.PasswordHash, &record.PasswordSalt, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return userRecord{}, errInvalidCredentials
	}
	if err != nil {
		return userRecord{}, fmt.Errorf("query user: %w", err)
	}

	if hashPassword(password, record.PasswordSalt) != record.PasswordHash {
		return userRecord{}, errInvalidCredentials
	}

	return record, nil
}

func normalizeUsername(username string) (string, error) {
	trimmed := strings.TrimSpace(username)
	if len(trimmed) < 3 {
		return "", errors.New("username must be at least 3 characters")
	}
	if len(trimmed) > 64 {
		return "", errors.New("username must be at most 64 characters")
	}
	return trimmed, nil
}

func randomSalt() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func hashPassword(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + ":" + password))
	return hex.EncodeToString(sum[:])
}
