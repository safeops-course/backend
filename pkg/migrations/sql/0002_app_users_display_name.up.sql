-- 0002 (expand): an optional display name for each user.
-- Expand only: a new NULLable column with no default - instant on PostgreSQL, no table rewrite, and
-- the previous release (which never names it in its INSERT or SELECT) keeps working on this schema.
-- So rolling back the image after this migration needs no rollback of the data.
-- The read side is switched on separately (FEATURE_DISPLAY_NAME); a later release may make the
-- column required - the contract step - once every writer sets it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE app_users ADD COLUMN IF NOT EXISTS display_name VARCHAR(64);
