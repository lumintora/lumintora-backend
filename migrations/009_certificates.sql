CREATE TABLE IF NOT EXISTS certificates (
    id         UUID         PRIMARY KEY DEFAULT uuid_generate_v4(),
    cert_id    VARCHAR(24)  NOT NULL UNIQUE,
    user_id    UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    path_id    UUID         REFERENCES learning_paths(id) ON DELETE SET NULL,
    path_title TEXT         NOT NULL,
    user_name  TEXT         NOT NULL,
    issued_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- Partial unique index so idempotency check works even after a path is deleted
CREATE UNIQUE INDEX IF NOT EXISTS idx_certificates_user_path
    ON certificates(user_id, path_id) WHERE path_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_certificates_cert_id ON certificates(cert_id);
CREATE INDEX IF NOT EXISTS idx_certificates_user_id ON certificates(user_id);
