CREATE TABLE IF NOT EXISTS career_applications (
    id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name       VARCHAR(255)  NOT NULL,
    email      VARCHAR(320)  NOT NULL,
    college    VARCHAR(300)  NOT NULL,
    year       VARCHAR(50)   NOT NULL,
    linkedin   TEXT,
    role       VARCHAR(50)   NOT NULL,
    answers    JSONB         NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_career_apps_role    ON career_applications(role);
CREATE INDEX IF NOT EXISTS idx_career_apps_email   ON career_applications(email);
CREATE INDEX IF NOT EXISTS idx_career_apps_created ON career_applications(created_at DESC);
