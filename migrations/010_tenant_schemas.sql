-- 010: Schema-per-tenant.
--
-- Each user gets their own Postgres schema `tenant_<uuid>` that holds their own
-- learning tables (learning_paths, modules, quiz_questions, user_module_progress,
-- xp_transactions, path_adaptations). Global tables stay in `public`:
--   users, waitlist, career_applications, feedback, certificates, leaderboard.
--
-- Idempotent: the helper is CREATE OR REPLACE, all tables are IF NOT EXISTS, and
-- the one-time data move only runs while the legacy public.learning_paths exists.

-- Certificates stay global so /verify can look one up by cert_id without knowing
-- the tenant. Its FK to learning_paths is dropped (path_id becomes a soft ref;
-- the certificate already snapshots path_title/user_name).
ALTER TABLE public.certificates DROP CONSTRAINT IF EXISTS certificates_path_id_fkey;

-- create_tenant_schema(user_id): build a user's private schema + tables. Called
-- from this migration for existing users and from auth-service at signup.
CREATE OR REPLACE FUNCTION create_tenant_schema(p_user_id UUID)
RETURNS void
LANGUAGE plpgsql
AS $fn$
DECLARE
  s text := 'tenant_' || replace(p_user_id::text, '-', '');
BEGIN
  EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', s);

  EXECUTE format($ddl$
    CREATE TABLE IF NOT EXISTS %1$I.learning_paths (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
      title varchar(500) NOT NULL,
      description text,
      goal text NOT NULL,
      topic varchar(255) NOT NULL,
      level varchar(50) DEFAULT 'beginner',
      status varchar(50) DEFAULT 'active',
      progress integer DEFAULT 0,
      total_modules integer DEFAULT 0,
      completed_modules integer DEFAULT 0,
      estimated_hours integer DEFAULT 0,
      tags text[],
      created_at timestamptz DEFAULT now(),
      updated_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_learning_paths_user_id ON %1$I.learning_paths(user_id);

    CREATE TABLE IF NOT EXISTS %1$I.modules (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      path_id uuid REFERENCES %1$I.learning_paths(id) ON DELETE CASCADE,
      title varchar(500) NOT NULL,
      description text,
      content text,
      type varchar(50) DEFAULT 'lesson',
      order_index integer NOT NULL,
      duration_minutes integer DEFAULT 15,
      xp_reward integer DEFAULT 10,
      status varchar(50) DEFAULT 'locked',
      difficulty varchar(50) DEFAULT 'medium',
      created_at timestamptz DEFAULT now(),
      updated_at timestamptz DEFAULT now(),
      source varchar(20) DEFAULT 'initial',
      adaptive_reason text
    );
    CREATE INDEX IF NOT EXISTS idx_modules_path_id ON %1$I.modules(path_id);
    CREATE INDEX IF NOT EXISTS idx_modules_source ON %1$I.modules(source);

    CREATE TABLE IF NOT EXISTS %1$I.quiz_questions (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      module_id uuid REFERENCES %1$I.modules(id) ON DELETE CASCADE,
      question text NOT NULL,
      options jsonb NOT NULL,
      correct_option integer NOT NULL,
      explanation text,
      order_index integer NOT NULL
    );

    CREATE TABLE IF NOT EXISTS %1$I.user_module_progress (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
      module_id uuid REFERENCES %1$I.modules(id) ON DELETE CASCADE,
      path_id uuid REFERENCES %1$I.learning_paths(id) ON DELETE CASCADE,
      status varchar(50) DEFAULT 'not_started',
      score integer DEFAULT 0,
      time_spent_seconds integer DEFAULT 0,
      attempts integer DEFAULT 0,
      completed_at timestamptz,
      started_at timestamptz,
      difficulty_feedback varchar(10),
      UNIQUE(user_id, module_id)
    );
    CREATE INDEX IF NOT EXISTS idx_user_module_progress_user_id ON %1$I.user_module_progress(user_id);
    CREATE INDEX IF NOT EXISTS idx_user_module_progress_module_id ON %1$I.user_module_progress(module_id);

    CREATE TABLE IF NOT EXISTS %1$I.xp_transactions (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
      amount integer NOT NULL,
      reason varchar(255),
      module_id uuid REFERENCES %1$I.modules(id),
      created_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_xp_transactions_user_day ON %1$I.xp_transactions(user_id, created_at);
    CREATE INDEX IF NOT EXISTS idx_xp_transactions_user_id ON %1$I.xp_transactions(user_id);

    CREATE TABLE IF NOT EXISTS %1$I.path_adaptations (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      path_id uuid REFERENCES %1$I.learning_paths(id) ON DELETE CASCADE,
      user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
      trigger_module_id uuid REFERENCES %1$I.modules(id) ON DELETE SET NULL,
      direction varchar(20) NOT NULL,
      reason text,
      created_module_id uuid REFERENCES %1$I.modules(id) ON DELETE SET NULL,
      created_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_path_adaptations_path ON %1$I.path_adaptations(path_id, user_id);
  $ddl$, s);
END;
$fn$;

-- One-time move of existing users' rows from the legacy public tables into each
-- user's own schema, then drop the legacy public copies.
DO $mig$
DECLARE
  u RECORD;
  s text;
BEGIN
  IF EXISTS (SELECT 1 FROM information_schema.tables
             WHERE table_schema = 'public' AND table_name = 'learning_paths') THEN

    FOR u IN SELECT id FROM public.users LOOP
      s := 'tenant_' || replace(u.id::text, '-', '');
      PERFORM create_tenant_schema(u.id);

      EXECUTE format(
        'INSERT INTO %1$I.learning_paths
           SELECT * FROM public.learning_paths WHERE user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);

      EXECUTE format(
        'INSERT INTO %1$I.modules
           SELECT m.* FROM public.modules m
             JOIN public.learning_paths p ON p.id = m.path_id
           WHERE p.user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);

      EXECUTE format(
        'INSERT INTO %1$I.quiz_questions
           SELECT q.* FROM public.quiz_questions q
             JOIN public.modules m ON m.id = q.module_id
             JOIN public.learning_paths p ON p.id = m.path_id
           WHERE p.user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);

      EXECUTE format(
        'INSERT INTO %1$I.user_module_progress
           SELECT * FROM public.user_module_progress WHERE user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);

      EXECUTE format(
        'INSERT INTO %1$I.xp_transactions
           SELECT * FROM public.xp_transactions WHERE user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);

      EXECUTE format(
        'INSERT INTO %1$I.path_adaptations
           SELECT * FROM public.path_adaptations WHERE user_id = %2$L
         ON CONFLICT (id) DO NOTHING', s, u.id);
    END LOOP;

    DROP TABLE IF EXISTS
      public.path_adaptations,
      public.quiz_questions,
      public.user_module_progress,
      public.xp_transactions,
      public.modules,
      public.learning_paths
    CASCADE;
  END IF;
END
$mig$;
