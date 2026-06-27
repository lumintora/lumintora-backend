-- Lumintora · adaptive learning + richer content
-- Idempotent: re-applied safely on every server start.

-- Where a module came from: the initial AI plan, or an adaptive insertion.
ALTER TABLE modules ADD COLUMN IF NOT EXISTS source VARCHAR(20) DEFAULT 'initial'; -- initial | adaptive
-- Why an adaptive module was added (shown to the learner).
ALTER TABLE modules ADD COLUMN IF NOT EXISTS adaptive_reason TEXT;

-- The learner's self-rated difficulty for a module: 'easy' | 'good' | 'hard'.
ALTER TABLE user_module_progress ADD COLUMN IF NOT EXISTS difficulty_feedback VARCHAR(10);

-- A log of every adaptation we make to a path, so the journey can explain itself
-- and we never re-adapt for the same trigger twice in a row.
CREATE TABLE IF NOT EXISTS path_adaptations (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    path_id UUID REFERENCES learning_paths(id) ON DELETE CASCADE,
    user_id UUID REFERENCES users(id) ON DELETE CASCADE,
    trigger_module_id UUID REFERENCES modules(id) ON DELETE SET NULL,
    direction VARCHAR(20) NOT NULL,        -- remediate | advance | reinforce
    reason TEXT,
    created_module_id UUID REFERENCES modules(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_path_adaptations_path ON path_adaptations(path_id, user_id);
CREATE INDEX IF NOT EXISTS idx_modules_source ON modules(source);
