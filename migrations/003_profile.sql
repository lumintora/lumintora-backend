-- Profile fields: contact details + social media handles.
-- Idempotent so it is safe to run on every startup.

ALTER TABLE users ADD COLUMN IF NOT EXISTS phone     VARCHAR(40);
ALTER TABLE users ADD COLUMN IF NOT EXISTS bio       TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS location  VARCHAR(160);
ALTER TABLE users ADD COLUMN IF NOT EXISTS website   TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS twitter   VARCHAR(120);
ALTER TABLE users ADD COLUMN IF NOT EXISTS github    VARCHAR(120);
ALTER TABLE users ADD COLUMN IF NOT EXISTS linkedin  VARCHAR(200);

-- The contributions heatmap reads xp_transactions by day; this index keeps the
-- per-user, time-windowed aggregation fast.
CREATE INDEX IF NOT EXISTS idx_xp_transactions_user_day
    ON xp_transactions(user_id, created_at);
