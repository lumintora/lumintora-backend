-- 012: Lumintora Forge.
--
-- Forge turns forgotten technical knowledge into targeted practice. It adds:
--   * A GLOBAL engineering skill graph (public.forge_skills / forge_skill_prereqs)
--     — the catalog of topics and sub-skills, shared by every learner.
--   * A GLOBAL seeded content bank (public.forge_challenges) — diagnostic
--     questions, refresh activities and interview prompts. Seeded so the demo
--     works deterministically even when the AI worker is unavailable; AI is used
--     to *enhance* (personalise explanations, grade free-text) on top of it.
--   * PER-TENANT learner state (learner_skills, forge_sessions, forge_activities,
--     knowledge_events, misconceptions, refresh_plans, interview_sessions,
--     interview_evaluations) provisioned inside create_tenant_schema().
--
-- Idempotent: reference tables are IF NOT EXISTS, seeds use ON CONFLICT DO
-- NOTHING, create_tenant_schema is CREATE OR REPLACE (re-run for existing users
-- at the end so their schemas gain the new tables), and it keeps every legacy
-- Lumintora table so existing learning-path functionality is preserved.

-- ------------------------------------------------------------------------------
-- GLOBAL: engineering skill graph
-- ------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.forge_skills (
  key         text PRIMARY KEY,                 -- e.g. 'redis.distributed_locks'
  name        text NOT NULL,                    -- 'Distributed Locks'
  parent_key  text REFERENCES public.forge_skills(key) ON DELETE CASCADE,
  domain      text NOT NULL DEFAULT 'backend',  -- 'backend', 'system_design', ...
  description text,
  order_index integer NOT NULL DEFAULT 0,
  created_at  timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_forge_skills_parent ON public.forge_skills(parent_key);

CREATE TABLE IF NOT EXISTS public.forge_skill_prereqs (
  skill_key  text NOT NULL REFERENCES public.forge_skills(key) ON DELETE CASCADE,
  prereq_key text NOT NULL REFERENCES public.forge_skills(key) ON DELETE CASCADE,
  PRIMARY KEY (skill_key, prereq_key)
);

-- ------------------------------------------------------------------------------
-- GLOBAL: seeded content bank
-- ------------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.forge_challenges (
  id                uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
  slug              text UNIQUE NOT NULL,        -- stable id for idempotent seeds
  skill_key         text NOT NULL REFERENCES public.forge_skills(key) ON DELETE CASCADE,
  mode              text NOT NULL DEFAULT 'refresh',   -- diagnostic | refresh | interview
  kind              text NOT NULL DEFAULT 'open',      -- open | concept | example | scenario | recall | code | mcq | followup
  difficulty        text NOT NULL DEFAULT 'medium',    -- beginner | medium | advanced
  title             text,
  prompt            text NOT NULL,               -- the question / instruction shown to the learner
  body              text,                        -- concept / example prose (for concept & example kinds)
  reference_answer  text,                        -- model answer, used to ground AI grading + fallback explanation
  expected_concepts jsonb NOT NULL DEFAULT '[]', -- keywords for deterministic fallback scoring
  hints             jsonb NOT NULL DEFAULT '[]', -- ordered hints (Socratic ladder)
  explanation       text,                        -- shown after answering
  order_index       integer NOT NULL DEFAULT 0,
  created_at        timestamptz DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_forge_challenges_skill ON public.forge_challenges(skill_key, mode, order_index);

-- ------------------------------------------------------------------------------
-- PER-TENANT: learner state — folded into create_tenant_schema so both new and
-- existing users get every table. The legacy learning tables are preserved.
-- ------------------------------------------------------------------------------
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

  -- Legacy Lumintora learning tables (unchanged — preserves existing product).
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

  -- Forge: per-learner skill state. mastery/confidence/decay are 0-100 signals
  -- computed deterministically by the backend (never written directly by an LLM).
  EXECUTE format($ddl$
    CREATE TABLE IF NOT EXISTS %1$I.learner_skills (
      skill_key         text PRIMARY KEY,
      mastery           integer NOT NULL DEFAULT 0,   -- 0-100
      confidence        integer NOT NULL DEFAULT 30,  -- 0-100, grows with attempts
      attempts          integer NOT NULL DEFAULT 0,
      correct_attempts  integer NOT NULL DEFAULT 0,
      last_practiced_at timestamptz,
      last_correct_at   timestamptz,
      decay_signal      integer NOT NULL DEFAULT 0,   -- 0-100, higher = more forgotten
      misconception_count integer NOT NULL DEFAULT 0,
      updated_at        timestamptz DEFAULT now()
    );

    CREATE TABLE IF NOT EXISTS %1$I.forge_sessions (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
      mode text NOT NULL DEFAULT 'refresh',      -- refresh | master | interview
      topic_key text,                            -- top-level skill, e.g. 'redis'
      status text NOT NULL DEFAULT 'diagnostic', -- diagnostic | active | completed | abandoned
      duration_minutes integer NOT NULL DEFAULT 10,
      diagnostic jsonb DEFAULT '[]',             -- diagnostic questions + classification
      plan jsonb DEFAULT '[]',                   -- ordered refresh plan snapshot
      summary jsonb DEFAULT '{}',                -- completion summary (before/after)
      started_at timestamptz DEFAULT now(),
      completed_at timestamptz
    );
    CREATE INDEX IF NOT EXISTS idx_forge_sessions_user ON %1$I.forge_sessions(user_id, status);

    CREATE TABLE IF NOT EXISTS %1$I.forge_activities (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      session_id uuid NOT NULL REFERENCES %1$I.forge_sessions(id) ON DELETE CASCADE,
      skill_key text NOT NULL,
      kind text NOT NULL DEFAULT 'scenario',     -- concept | example | scenario | recall | code | mcq
      phase text NOT NULL DEFAULT 'practice',    -- diagnostic | practice
      prompt text NOT NULL,
      concept text,                              -- 30-second explanation shown before the challenge
      example text,
      hints jsonb DEFAULT '[]',
      reference_answer text,
      expected_concepts jsonb DEFAULT '[]',
      explanation text,
      response text,
      score integer,                             -- 0-100 for this activity
      evaluation jsonb,                          -- structured grader output
      mastery_before integer,
      mastery_after integer,
      order_index integer NOT NULL DEFAULT 0,
      created_at timestamptz DEFAULT now(),
      answered_at timestamptz
    );
    CREATE INDEX IF NOT EXISTS idx_forge_activities_session ON %1$I.forge_activities(session_id, order_index);

    CREATE TABLE IF NOT EXISTS %1$I.knowledge_events (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid REFERENCES public.users(id) ON DELETE CASCADE,
      event_type text NOT NULL,                  -- session_started, diagnostic_answered, ...
      skill_key text,
      session_id uuid,
      payload jsonb DEFAULT '{}',
      created_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_knowledge_events_user ON %1$I.knowledge_events(user_id, created_at);

    CREATE TABLE IF NOT EXISTS %1$I.misconceptions (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      skill_key text NOT NULL,
      label text NOT NULL,
      detail text,
      session_id uuid,
      resolved boolean NOT NULL DEFAULT false,
      created_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_misconceptions_skill ON %1$I.misconceptions(skill_key, resolved);

    CREATE TABLE IF NOT EXISTS %1$I.refresh_plans (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      session_id uuid NOT NULL REFERENCES %1$I.forge_sessions(id) ON DELETE CASCADE,
      skill_key text NOT NULL,
      priority integer NOT NULL DEFAULT 0,       -- lower = practice first
      reason text,
      target_minutes integer DEFAULT 3,
      status text NOT NULL DEFAULT 'pending',    -- pending | active | done
      created_at timestamptz DEFAULT now()
    );
    CREATE INDEX IF NOT EXISTS idx_refresh_plans_session ON %1$I.refresh_plans(session_id, priority);

    CREATE TABLE IF NOT EXISTS %1$I.interview_sessions (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      user_id uuid NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
      topic_key text,
      prompt text NOT NULL,
      status text NOT NULL DEFAULT 'active',      -- active | completed
      transcript jsonb DEFAULT '[]',             -- [{role, content}]
      started_at timestamptz DEFAULT now(),
      completed_at timestamptz
    );

    CREATE TABLE IF NOT EXISTS %1$I.interview_evaluations (
      id uuid PRIMARY KEY DEFAULT public.uuid_generate_v4(),
      interview_id uuid NOT NULL REFERENCES %1$I.interview_sessions(id) ON DELETE CASCADE,
      dimensions jsonb NOT NULL DEFAULT '{}',    -- {requirements: 0-100, api_design: ...}
      strengths jsonb DEFAULT '[]',
      gaps jsonb DEFAULT '[]',
      recommended text,
      created_at timestamptz DEFAULT now()
    );
  $ddl$, s);
END;
$fn$;

-- Re-provision every existing user's schema so it gains the Forge tables.
DO $mig$
DECLARE u RECORD;
BEGIN
  FOR u IN SELECT id FROM public.users LOOP
    PERFORM create_tenant_schema(u.id);
  END LOOP;
END
$mig$;
