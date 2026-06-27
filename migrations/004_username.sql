-- Add unique username with format constraint.
-- Nullable so existing rows (if any) are not broken; enforced at app level on new signups.

ALTER TABLE users ADD COLUMN IF NOT EXISTS username VARCHAR(30);

-- ADD CONSTRAINT IF NOT EXISTS does not exist in PostgreSQL; use a DO block instead.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'users_username_format'
  ) THEN
    ALTER TABLE users ADD CONSTRAINT users_username_format
      CHECK (username ~ '^[a-zA-Z0-9_-]+$');
  END IF;
END;
$$;

CREATE UNIQUE INDEX IF NOT EXISTS users_username_unique
    ON users(username)
    WHERE username IS NOT NULL;
