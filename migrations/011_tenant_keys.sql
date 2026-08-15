-- 011: Short alphanumeric tenant schema names (no "tenant_" prefix).
--
-- Migration 010 named each user's schema tenant_<32-hex-uuid>. Replace that with a
-- short, opaque alphanumeric id used directly as the schema name:
--   users.tenant_key — unique 10-char id matching [a-z][a-z0-9]{9}
--   schema name      — that key, e.g. "a1b2c3d4e5"
--
-- The id starts with a letter so it is a valid unquoted SQL identifier (the app
-- interpolates it as schema.table). Idempotent: column/constraint are guarded,
-- functions are CREATE OR REPLACE, backfill only assigns a key when missing, and
-- the rename only fires while the old tenant_<hex> schema still exists.

ALTER TABLE public.users ADD COLUMN IF NOT EXISTS tenant_key text;

DO $c$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_tenant_key_key') THEN
    ALTER TABLE public.users ADD CONSTRAINT users_tenant_key_key UNIQUE (tenant_key);
  END IF;
END
$c$;

-- gen_tenant_key(): a random 10-char id, first char a letter then 9 alphanumerics,
-- retried until unique across users.tenant_key.
CREATE OR REPLACE FUNCTION gen_tenant_key()
RETURNS text
LANGUAGE plpgsql
AS $g$
DECLARE
  alpha constant text := 'abcdefghijklmnopqrstuvwxyz';
  alnum constant text := 'abcdefghijklmnopqrstuvwxyz0123456789';
  k text;
  i integer;
BEGIN
  LOOP
    k := substr(alpha, 1 + floor(random() * 26)::int, 1);
    FOR i IN 1..9 LOOP
      k := k || substr(alnum, 1 + floor(random() * 36)::int, 1);
    END LOOP;
    EXIT WHEN NOT EXISTS (SELECT 1 FROM public.users WHERE tenant_key = k);
  END LOOP;
  RETURN k;
END;
$g$;

-- create_tenant_schema(user_id): assign a tenant_key if the user has none, then
-- build their private schema (named by the key) with all learning tables.
-- Replaces the uuid-named version from migration 010.
CREATE OR REPLACE FUNCTION create_tenant_schema(p_user_id UUID)
RETURNS void
LANGUAGE plpgsql
AS $fn$
DECLARE
  s text;
BEGIN
  UPDATE public.users SET tenant_key = gen_tenant_key()
   WHERE id = p_user_id AND tenant_key IS NULL;

  SELECT tenant_key INTO s FROM public.users WHERE id = p_user_id;
  IF s IS NULL THEN
    RAISE EXCEPTION 'create_tenant_schema: no such user %', p_user_id;
  END IF;

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

-- Backfill a tenant_key for every existing user (one at a time so uniqueness
-- holds), then rename their long uuid-named schema to the new key. ALTER SCHEMA
-- RENAME keeps every table, index and FK intact, so no data moves.
DO $mig$
DECLARE
  u RECORD;
  olds text;
  news text;
BEGIN
  FOR u IN SELECT id FROM public.users WHERE tenant_key IS NULL LOOP
    UPDATE public.users SET tenant_key = gen_tenant_key() WHERE id = u.id AND tenant_key IS NULL;
  END LOOP;

  FOR u IN SELECT id, tenant_key FROM public.users WHERE tenant_key IS NOT NULL LOOP
    olds := 'tenant_' || replace(u.id::text, '-', '');
    news := u.tenant_key;
    IF EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = olds)
       AND NOT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = news) THEN
      EXECUTE format('ALTER SCHEMA %I RENAME TO %I', olds, news);
    END IF;
  END LOOP;
END
$mig$;
