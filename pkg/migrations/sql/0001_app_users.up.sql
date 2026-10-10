-- 0001: the users table as the backend created it at startup before versioned migrations.
-- IF NOT EXISTS on purpose: databases that already have the table (every environment before this
-- change) are not touched; golang-migrate records them at version 1 and moves on.
-- Every migration starts with this line (migrations_test.go checks it): its DDL waits at most 5 s for
-- a table lock, then fails - see the package comment in migrations.go for why.
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS app_users (
  id BIGSERIAL PRIMARY KEY,
  username VARCHAR(64) NOT NULL,
  password_hash TEXT NOT NULL,
  password_salt TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS app_users_username_lower_uq
  ON app_users ((lower(username)));
