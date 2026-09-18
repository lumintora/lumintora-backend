-- 013: Forge skill graph + seeded content bank (global reference data).
--
-- Idempotent via ON CONFLICT DO NOTHING keyed on the natural id (skills.key,
-- challenges.slug). Safe to re-run on every startup.

-- ------------------------------------------------------------------------------
-- Skill graph (section 4). Top-level topics have parent_key = NULL.
-- ------------------------------------------------------------------------------
INSERT INTO public.forge_skills (key, name, parent_key, domain, description, order_index) VALUES
  ('python',                 'Python',              NULL,     'backend',       'Language fundamentals, async, performance, testing', 1),
  ('python.fundamentals',    'Fundamentals',        'python', 'backend',       'Core syntax, data model, idioms', 1),
  ('python.async',           'Async Programming',   'python', 'backend',       'asyncio, coroutines, event loop', 2),
  ('python.performance',     'Performance',         'python', 'backend',       'Profiling, hot paths, C extensions', 3),
  ('python.testing',         'Testing',             'python', 'backend',       'pytest, fixtures, mocking', 4),

  ('fastapi',                'FastAPI',             NULL,      'backend',      'Modern async Python web framework', 2),
  ('fastapi.routing',        'Routing',             'fastapi', 'backend',      'Path/query params, routers', 1),
  ('fastapi.di',             'Dependency Injection','fastapi', 'backend',      'Depends(), scoped resources', 2),
  ('fastapi.async',          'Async',               'fastapi', 'backend',      'async endpoints, background tasks', 3),
  ('fastapi.production',     'Production Patterns', 'fastapi', 'backend',      'Workers, lifespans, observability', 4),

  ('postgresql',             'PostgreSQL',          NULL,          'backend',  'Relational database internals & usage', 3),
  ('postgresql.sql',         'SQL',                 'postgresql',  'backend',  'Joins, aggregates, CTEs, window fns', 1),
  ('postgresql.indexing',    'Indexing',            'postgresql',  'backend',  'B-tree, partial, covering, GIN', 2),
  ('postgresql.transactions','Transactions',        'postgresql',  'backend',  'Isolation levels, MVCC, locking', 3),
  ('postgresql.query_opt',   'Query Optimization',  'postgresql',  'backend',  'EXPLAIN, planner, N+1', 4),
  ('postgresql.replication', 'Replication',         'postgresql',  'backend',  'Streaming, failover, read replicas', 5),

  ('redis',                  'Redis',               NULL,     'backend',       'In-memory data store: caching & coordination', 4),
  ('redis.fundamentals',     'Fundamentals',        'redis',  'backend',       'What Redis is and when to reach for it', 1),
  ('redis.data_structures',  'Data Structures',     'redis',  'backend',       'Strings, hashes, lists, sets, sorted sets', 2),
  ('redis.caching',          'Caching',             'redis',  'backend',       'Cache-aside, invalidation, stampede', 3),
  ('redis.ttl',              'TTL',                 'redis',  'backend',       'Key expiry and expiration semantics', 4),
  ('redis.eviction',         'Eviction',            'redis',  'backend',       'maxmemory and eviction policies', 5),
  ('redis.pubsub',           'Pub/Sub',             'redis',  'backend',       'Channels, fan-out messaging', 6),
  ('redis.distributed_locks','Distributed Locks',   'redis',  'backend',       'Coordination across processes', 7),
  ('redis.streams',          'Redis Streams',       'redis',  'backend',       'Append-only log, consumer groups', 8),

  ('system_design',          'System Design',       NULL,            'system_design', 'Designing scalable, reliable systems', 5),
  ('system_design.scalability','Scalability',       'system_design', 'system_design', 'Horizontal scaling, sharding', 1),
  ('system_design.caching',  'Caching',             'system_design', 'system_design', 'Where caches live in an architecture', 2),
  ('system_design.queues',   'Queues',              'system_design', 'system_design', 'Async processing, backpressure', 3),
  ('system_design.databases','Databases',           'system_design', 'system_design', 'SQL vs NoSQL, partitioning', 4),
  ('system_design.consistency','Consistency',       'system_design', 'system_design', 'CAP, consistency models', 5),
  ('system_design.failure',  'Failure Handling',    'system_design', 'system_design', 'Retries, idempotency, timeouts', 6),

  ('kafka',                  'Kafka',               NULL,    'backend',        'Distributed event streaming', 6),
  ('kafka.fundamentals',     'Fundamentals',        'kafka', 'backend',        'Topics, partitions, offsets', 1),
  ('kafka.delivery',         'Delivery Semantics',  'kafka', 'backend',        'At-least/at-most/exactly-once', 2),
  ('kafka.consumers',        'Consumer Groups',     'kafka', 'backend',        'Rebalancing, lag', 3),

  ('kubernetes',             'Kubernetes',          NULL,         'backend',   'Container orchestration', 7),
  ('kubernetes.workloads',   'Workloads',           'kubernetes', 'backend',   'Pods, deployments, statefulsets', 1),
  ('kubernetes.networking',  'Networking',          'kubernetes', 'backend',   'Services, ingress, DNS', 2),
  ('kubernetes.scaling',     'Scaling',             'kubernetes', 'backend',   'HPA, resource limits', 3)
ON CONFLICT (key) DO NOTHING;

-- A few prerequisite edges (deterministic progression constraints).
INSERT INTO public.forge_skill_prereqs (skill_key, prereq_key) VALUES
  ('redis.caching',           'redis.fundamentals'),
  ('redis.ttl',               'redis.fundamentals'),
  ('redis.eviction',          'redis.ttl'),
  ('redis.distributed_locks', 'redis.fundamentals'),
  ('redis.streams',           'redis.data_structures')
ON CONFLICT DO NOTHING;

-- ------------------------------------------------------------------------------
-- Redis diagnostic (section 2). Open-ended; graded into per-skill signals.
-- ------------------------------------------------------------------------------
INSERT INTO public.forge_challenges (slug, skill_key, mode, kind, difficulty, prompt, reference_answer, expected_concepts, explanation, order_index) VALUES
  ('redis.diag.1', 'redis.fundamentals', 'diagnostic', 'open', 'beginner',
   'What problem does Redis solve, and why would you add it to a system?',
   'Redis is an in-memory key-value store used to serve data with sub-millisecond latency — caching hot data, sessions, rate limits, queues and coordination — offloading slower primary databases.',
   '["in-memory","key-value","cache","latency","fast","session","offload database","throughput"]',
   'Redis keeps data in RAM, so reads/writes are sub-millisecond. It''s used to cache hot data, hold sessions/rate-limits, and coordinate work — not as a durable system of record.', 1),

  ('redis.diag.2', 'redis.ttl', 'diagnostic', 'open', 'medium',
   'What happens when a Redis key''s TTL expires?',
   'An expired key is logically gone: reads return nil. Redis removes it lazily on access and also samples keys in the background (active expiration) to reclaim memory.',
   '["expired","removed","nil","lazy expiration","active expiration","background","reclaim memory"]',
   'Expiry is both lazy (removed when next accessed) and active (a background cycle samples and evicts expired keys). Either way the key reads as nil once expired.', 2),

  ('redis.diag.3', 'redis.caching', 'diagnostic', 'open', 'advanced',
   'A popular cache entry expires and thousands of requests hit the database at once. How would you prevent this cache stampede?',
   'Prevent the thundering herd: add TTL jitter, coalesce requests behind a per-key lock/mutex so only one recomputes, use stale-while-revalidate (serve stale while one worker refreshes), or refresh early before expiry.',
   '["stampede","thundering herd","lock","mutex","coalesce","single flight","stale-while-revalidate","ttl jitter","early recompute"]',
   'The fix is to stop N concurrent misses from all recomputing: coalesce them behind a lock (single-flight), serve stale-while-revalidate, and jitter TTLs so keys don''t all expire together.', 3),

  ('redis.diag.4', 'redis.fundamentals', 'diagnostic', 'open', 'medium',
   'When would you use Redis instead of PostgreSQL — and when would you NOT?',
   'Use Redis for ephemeral, high-throughput, low-latency data (cache, sessions, counters, rate limits, ephemeral queues). Don''t use it as the durable source of truth for relational data needing ACID transactions and complex queries.',
   '["ephemeral","cache","low latency","high throughput","not durable","source of truth","acid","relational"]',
   'Redis wins for ephemeral, latency-sensitive, high-throughput access. Postgres wins for durable, relational, transactional data. They''re complementary, not substitutes.', 4),

  ('redis.diag.5', 'redis.distributed_locks', 'diagnostic', 'open', 'advanced',
   'How would you implement a distributed lock in Redis, and what makes a naive one unsafe?',
   'Use SET key token NX PX <ttl>: acquire only if absent, with a TTL so a crashed holder auto-releases, and a unique token so you only release your own lock (via a Lua compare-and-delete). Naive locks without a TTL deadlock on crash; without a unique token one client deletes another''s lock; and clock/TTL expiry can still cause two holders (fencing tokens / Redlock address this).',
   '["setnx","set nx px","ttl","expiry","unique token","lua","compare and delete","fencing token","redlock","crash"]',
   'Safe locking needs three things: NX to acquire atomically, a TTL so a crashed holder is released, and a unique token released via a Lua compare-and-delete so you never delete someone else''s lock. Fencing tokens guard against expiry races.', 5)
ON CONFLICT (slug) DO NOTHING;

-- ------------------------------------------------------------------------------
-- Redis refresh activities. Each has a 30s concept, an example, and a challenge
-- the learner answers. Grouped by sub-skill; the weakest-first plan pulls these.
-- ------------------------------------------------------------------------------
INSERT INTO public.forge_challenges (slug, skill_key, mode, kind, difficulty, title, body, prompt, reference_answer, expected_concepts, hints, explanation, order_index) VALUES
  ('redis.act.locks.1', 'redis.distributed_locks', 'refresh', 'concept', 'advanced',
   'Locks that survive a crash',
   'A distributed lock lets one process do work while others wait. In Redis: `SET lock:<name> <token> NX PX 30000`. NX means "only if it doesn''t exist" (atomic acquire). PX sets a TTL so if the holder crashes, the lock auto-releases instead of deadlocking. The `<token>` is a random value unique to this holder.',
   'You wrote `SETNX lock foo` then `EXPIRE lock 30`. Why is this unsafe, and how do you fix it in one command?',
   'Two commands aren''t atomic: if the process crashes between SETNX and EXPIRE, the lock has no TTL and deadlocks forever. Fix: `SET lock <token> NX PX 30000` acquires and sets the TTL atomically.',
   '["atomic","crash","no ttl","deadlock","set nx px","one command"]',
   '["What happens if the process dies right after SETNX succeeds?","Is there a single command that sets the value AND the expiry together?"]',
   'SETNX + EXPIRE is a race: a crash in between leaves a lock with no TTL that never releases. `SET key token NX PX ttl` does both atomically.', 1),

  ('redis.act.locks.2', 'redis.distributed_locks', 'refresh', 'scenario', 'advanced',
   'Releasing safely',
   'To release, you must delete only YOUR lock. If your work ran long and the TTL expired, another client may now hold the lock — a plain `DEL` would delete theirs.',
   'Client A acquires a lock with a 30s TTL but its work takes 40s. At 30s the lock expires and Client B acquires it. At 40s Client A finishes and runs `DEL lock`. What goes wrong, and how do you prevent it?',
   'Client A deletes Client B''s lock, so now two clients believe they hold it. Prevent it by releasing with a compare-and-delete: a Lua script that only DELs if the stored token equals A''s token. Fencing tokens (monotonic ids checked by the resource) prevent the stale holder from acting at all.',
   '["compare and delete","lua","token match","fencing token","deletes another lock","two holders"]',
   '["Whose lock is Client A actually deleting at 40s?","How can release check that the lock still belongs to A before deleting?"]',
   'Release must be conditional: a Lua compare-and-delete that checks the token first. Otherwise a slow holder deletes the next owner''s lock. Fencing tokens stop the stale holder from mutating the resource.', 2),

  ('redis.act.locks.3', 'redis.distributed_locks', 'refresh', 'recall', 'medium',
   'Recall: the three properties',
   'Recall the anatomy of a safe single-instance Redis lock.',
   'Name the three things a safe Redis lock needs and the Redis command that provides them.',
   'Atomic acquire (NX), auto-release on crash (PX/TTL), and ownership so you only release your own (unique token + Lua compare-and-delete). Command: SET key token NX PX ttl.',
   '["nx","ttl","px","unique token","lua","compare and delete"]',
   '["One prevents deadlock on crash. One prevents deleting someone else''s lock. One makes acquire atomic."]',
   'Atomic acquire (NX), TTL for crash safety (PX), and a unique token released via Lua compare-and-delete for ownership.', 3),

  ('redis.act.stampede.1', 'redis.caching', 'refresh', 'concept', 'advanced',
   'Cache stampede',
   'A stampede (thundering herd) happens when a hot key expires and every concurrent request misses and recomputes at once, hammering the DB. Three defenses: (1) TTL jitter so keys don''t expire together, (2) single-flight — coalesce concurrent misses behind a per-key lock so only one recomputes, (3) stale-while-revalidate — serve the stale value while one worker refreshes in the background.',
   'Your product page cache has a fixed 60s TTL. Every 60s, latency spikes and the DB CPU jumps. Explain why, and give two independent fixes.',
   'All requests share one key that expires at the same instant, so at each 60s boundary thousands miss simultaneously and recompute against the DB (stampede). Fixes: add TTL jitter (e.g. 60s ± random), and coalesce misses behind a per-key lock / serve stale-while-revalidate so only one request recomputes.',
   '["stampede","thundering herd","expire together","jitter","single flight","lock","coalesce","stale-while-revalidate"]',
   '["What do all the cached requests have in common at the 60s mark?","How can you make only ONE request rebuild the value?"]',
   'A single shared TTL makes every request miss at the same instant. Jitter spreads expiries; single-flight/stale-while-revalidate ensures only one request rebuilds the value.', 1),

  ('redis.act.stampede.2', 'redis.caching', 'refresh', 'recall', 'medium',
   'Recall: single-flight',
   'Recall the one-line idea behind stopping a stampede when a hot key misses.',
   'In one sentence: how does "single-flight" (request coalescing) stop a cache stampede, and what Redis primitive implements it?',
   'Single-flight lets only the first request on a missing key recompute while the rest wait for and reuse that result, instead of all recomputing; a short-lived per-key Redis lock (SET NX PX) elects that single recomputing request.',
   '["single flight","coalesce","only one","recompute","wait","lock","set nx","reuse"]',
   '["What should the other requests do while one rebuilds the value?"]',
   'Single-flight coalesces concurrent misses: one request rebuilds the value (elected via a short per-key SET NX lock) while the others wait and reuse it.', 2),

  ('redis.act.ttl.1', 'redis.ttl', 'refresh', 'scenario', 'medium',
   'Expiry semantics',
   'Redis expires keys two ways: lazily (checked and removed on access) and actively (a background cycle samples keys and removes expired ones). A key with a TTL reads as nil once expired even before the background cycle removes it.',
   'You SET a key with a 10s TTL and never read it again. Is the memory freed exactly at 10s? Explain how Redis actually reclaims it.',
   'Not necessarily at exactly 10s. Because nothing accesses it, lazy expiration never fires; the background active-expiration cycle samples keys periodically and removes expired ones, so memory is reclaimed shortly after — not instantly. A read after 10s always returns nil regardless.',
   '["lazy","active expiration","background","sample","not exactly","nil","reclaim"]',
   '["Lazy expiry only triggers on access — but nothing accesses this key.","What background process eventually reclaims untouched expired keys?"]',
   'Untouched keys aren''t freed the instant they expire — the active-expiration background cycle reclaims them shortly after. Reads still return nil immediately at expiry.', 1),

  ('redis.act.ttl.2', 'redis.ttl', 'refresh', 'recall', 'beginner',
   'Recall: the two expiry paths',
   'Recall how Redis actually removes expired keys.',
   'Name Redis'' two mechanisms for removing expired keys and when each fires.',
   'Lazy expiration removes a key when it''s next accessed; active expiration is a background cycle that periodically samples keys and removes expired ones. Together they guarantee an expired key never serves stale data and memory is eventually reclaimed.',
   '["lazy","on access","active","background","sample","periodic","reclaim"]',
   '["One fires only when you touch the key. One runs on its own in the background."]',
   'Lazy (on access) + active (background sampling) expiration. Lazy guarantees correctness on read; active reclaims memory for untouched keys.', 2),

  ('redis.act.eviction.1', 'redis.eviction', 'refresh', 'scenario', 'medium',
   'Eviction vs expiry',
   'Eviction is different from expiry. When Redis hits `maxmemory`, it evicts keys per its policy: noeviction (errors on write), allkeys-lru/lfu (evict any key), volatile-lru/ttl (only keys with a TTL). Expiry removes keys whose TTL passed; eviction removes keys to make room under memory pressure.',
   'Your Redis is a pure cache but is returning OOM write errors under load. Which maxmemory-policy is likely set, and what should it be?',
   'It''s likely `noeviction`, which errors instead of freeing memory. For a pure cache, use `allkeys-lru` (or allkeys-lfu) so Redis evicts the least-recently/frequently-used keys to make room. volatile-* only helps if every key has a TTL.',
   '["noeviction","oom","allkeys-lru","allkeys-lfu","maxmemory","policy","volatile"]',
   '["Which policy refuses to evict and returns errors instead?","For a cache, is it safe to let Redis drop old keys?"]',
   'OOM-on-write means noeviction. A cache should use allkeys-lru/lfu so Redis drops cold keys under pressure. volatile-* only evicts keys that have a TTL set.', 1),

  ('redis.act.datastructures.1', 'redis.data_structures', 'refresh', 'scenario', 'medium',
   'Pick the right structure',
   'Match the workload to the structure: sorted sets (ZSET) for leaderboards/rate-limit windows, hashes for objects, sets for membership/uniqueness, lists for queues, strings for counters/cache blobs.',
   'You need a real-time leaderboard of top players by score, with fast rank lookups. Which Redis data structure fits, and which two commands power it?',
   'A sorted set (ZSET): ZADD to insert/update a member''s score, and ZREVRANGE (top N) / ZREVRANK (a player''s rank). Scores keep members ordered so rank and top-N are O(log n).',
   '["sorted set","zset","zadd","zrange","zrevrange","zrank","zrevrank","score","rank"]',
   '["Which structure keeps members ordered by a numeric score?","How do you read the top N and one member''s rank?"]',
   'Leaderboards are the textbook ZSET use case: ZADD to score, ZREVRANGE for top-N, ZREVRANK for a player''s rank — all O(log n).', 1),

  ('redis.act.streams.1', 'redis.streams', 'refresh', 'concept', 'advanced',
   'Streams vs Pub/Sub',
   'Redis Streams are an append-only log with consumer groups: messages persist, each consumer group tracks its own position, and unacked messages can be redelivered (XADD/XREADGROUP/XACK). Pub/Sub is fire-and-forget — offline subscribers miss messages. Use Streams when you need durability and at-least-once delivery.',
   'You built a notification pipeline on Pub/Sub, but messages are lost when a consumer restarts. Why, and what Redis primitive fixes it?',
   'Pub/Sub is fire-and-forget: a subscriber that''s offline during publish never receives the message — there''s no backlog. Redis Streams fix this: XADD appends to a durable log, consumer groups (XREADGROUP) track per-group position and redeliver unacked (pending) entries after a restart, with XACK to confirm.',
   '["fire-and-forget","not durable","offline","lost","streams","consumer group","xadd","xreadgroup","xack","redeliver","pending"]',
   '["Does Pub/Sub store anything for a subscriber that''s temporarily down?","Which Redis feature keeps a durable log with per-consumer offsets?"]',
   'Pub/Sub drops messages for offline subscribers. Streams persist entries and use consumer groups to track offsets and redeliver unacked messages after a restart.', 1)
ON CONFLICT (slug) DO NOTHING;
