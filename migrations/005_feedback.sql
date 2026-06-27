CREATE TABLE IF NOT EXISTS feedback (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    name        VARCHAR(255) NOT NULL,
    email       VARCHAR(255) NOT NULL,
    college     VARCHAR(255),
    branch      VARCHAR(255),
    year        VARCHAR(50),
    category    VARCHAR(100) NOT NULL,
    subject     VARCHAR(500) NOT NULL,
    description TEXT NOT NULL,
    severity    VARCHAR(50),
    status      VARCHAR(50) DEFAULT 'open',
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_feedback_user_id ON feedback(user_id);
CREATE INDEX IF NOT EXISTS idx_feedback_created_at ON feedback(created_at DESC);
