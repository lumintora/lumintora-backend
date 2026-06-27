-- Add unique username with format constraint.
-- Nullable so existing rows (if any) are not broken; enforced at app level on new signups.

ALTER TABLE users ADD COLUMN IF NOT EXISTS username VARCHAR(30);

ALTER TABLE users ADD CONSTRAINT IF NOT EXISTS users_username_format
    CHECK (username ~ '^[a-zA-Z0-9_-]+$');

CREATE UNIQUE INDEX IF NOT EXISTS users_username_unique
    ON users(username)
    WHERE username IS NOT NULL;
