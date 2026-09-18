-- 014: Password reset (forgot-password) support.
--
-- A short-lived, single-use 6-digit code (bcrypt-hashed at rest) tied to a user.
-- The code is delivered by email when SMTP is configured; in local/dev it is
-- returned by the API and logged so the flow works without a mail server.
-- Global table (lives in public, like users).

CREATE TABLE IF NOT EXISTS public.password_resets (
  id         uuid PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id    uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
  code_hash  text NOT NULL,
  expires_at timestamptz NOT NULL,
  used       boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_password_resets_user ON public.password_resets(user_id, used);
