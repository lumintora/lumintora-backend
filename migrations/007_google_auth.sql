ALTER TABLE users ADD COLUMN IF NOT EXISTS google_id   VARCHAR(255);
ALTER TABLE users ADD COLUMN IF NOT EXISTS picture_url TEXT;

-- Unique index on google_id (sparse — only for rows that have one)
CREATE UNIQUE INDEX IF NOT EXISTS users_google_id_unique
    ON users(google_id)
    WHERE google_id IS NOT NULL;
