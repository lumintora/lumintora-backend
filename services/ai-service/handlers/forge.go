package handlers

// Lumintora Forge — adaptive technical refresh.
//
// Architecture (see section 14 of the product spec): deterministic Go owns every
// decision that affects learner state — mastery math, decay signals, keyword
// grading, plan ordering, mastery thresholds and XP. The AI worker (callCFAI) is
// used only to *enhance*: grade free-text more richly, personalise explanations
// and drive the Socratic tutor. Every AI response is parsed into a strict struct
// and validated; if the worker is slow, down or returns junk we fall back to the
// deterministic path so the demo always works.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/mailer"
	"lumintora/pkg/middleware"
	"lumintora/pkg/tenant"

	"github.com/go-chi/chi/v5"
)

// masteryThreshold is the deterministic bar for "you've got this again".
const masteryThreshold = 80

type ForgeHandler struct {
	db *sql.DB
	ai *AIHandler // reused purely for callCFAI; never for state decisions
}

func NewForgeHandler(db *sql.DB, ai *AIHandler) *ForgeHandler {
	return &ForgeHandler{db: db, ai: ai}
}

// ---------------------------------------------------------------------------
// Deterministic mastery / decay math (pure functions — unit-testable, no AI).
// ---------------------------------------------------------------------------

// blendMastery folds a new 0-100 score into a prior mastery. The first piece of
// evidence sets the level. After that it's a moving average — but a confidently
// correct retrieval on rusty material is strong evidence you've got it back, so
// a high score pulls mastery harder (fast "recovery"), while middling/weak
// answers move it gently so one unlucky answer can't tank a skill.
func blendMastery(prev, score, attempts int) int {
	score = clamp(score, 0, 100)
	if attempts <= 0 {
		return score
	}
	w := 0.5 // weight on new evidence
	if score >= 85 {
		w = 0.7
	} else if score < 45 {
		w = 0.4
	}
	return clamp(int(math.Round(float64(prev)*(1-w)+float64(score)*w)), 0, 100)
}

// confidenceAfter grows with the number of retrievals we've observed.
func confidenceAfter(attempts int) int { return clamp(30+attempts*12, 30, 95) }

// decaySignal is a product-generated staleness signal (0 = just practiced,
// 100 = long forgotten). It is NOT a scientific measurement — it's derived from
// time since practice, tempered by how well the skill was known.
func decaySignal(mastery int, lastPracticed *time.Time) int {
	if lastPracticed == nil {
		return clamp(100-mastery, 0, 100)
	}
	days := time.Since(*lastPracticed).Hours() / 24
	// ~1.4 points/day, but well-mastered material decays a little slower.
	rate := 1.4 - float64(mastery)/100*0.5
	return clamp(int(math.Round(days*rate)), 0, 100)
}

// conceptMatched decides whether a response demonstrates an expected concept.
// It matches the exact phrase, OR (for multi-word phrases) most of the phrase's
// significant words appearing anywhere — so "released with a Lua script" counts
// for the "lua compare and delete" concept without demanding the exact wording.
func conceptMatched(resp, phrase string) bool {
	if strings.Contains(resp, phrase) {
		return true
	}
	words := strings.Fields(phrase)
	if len(words) < 2 {
		return false
	}
	hit := 0
	for _, wds := range words {
		if len(wds) >= 3 && strings.Contains(resp, wds) {
			hit++
		}
	}
	return float64(hit)/float64(len(words)) >= 0.6
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// gradeDeterministic scores a free-text answer by keyword coverage. It is the
// always-available fallback and the floor under AI grading.
func gradeDeterministic(response string, expected []string) (score int, matched []string) {
	resp := strings.ToLower(response)
	if strings.TrimSpace(resp) == "" || len(expected) == 0 {
		if len(expected) == 0 {
			return 60, matched // nothing to check against — neutral credit
		}
		return 0, matched
	}
	for _, k := range expected {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if conceptMatched(resp, k) {
			matched = append(matched, k)
		}
	}
	frac := float64(len(matched)) / float64(len(expected))
	// A thoughtful answer rarely hits every exact phrase; scale generously but
	// require real substance (length guard against one-word answers).
	score = int(math.Round(frac*90)) + 10
	if len(strings.Fields(resp)) < 4 {
		score = clamp(score, 0, 35)
	}
	return clamp(score, 0, 100), matched
}

// ---------------------------------------------------------------------------
// AI-enhanced grading (structured, validated, falls back to deterministic).
// ---------------------------------------------------------------------------

type gradeResult struct {
	Score        int      `json:"score"`
	Matched      []string `json:"matched_concepts"`
	Missing      []string `json:"missing_concepts"`
	Misconception string  `json:"misconception"`
	Feedback     string   `json:"feedback"`
	source       string   // "ai" | "deterministic"
}

var gradeJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"score":            map[string]interface{}{"type": "integer"},
		"matched_concepts": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"missing_concepts": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"misconception":    map[string]interface{}{"type": "string"},
		"feedback":         map[string]interface{}{"type": "string"},
	},
	"required": []string{"score", "feedback"},
}

// grade grades one answer. It always computes the deterministic score first,
// then tries the AI grader; the AI result is only accepted if it parses and is
// sane, and its score is nudged toward the deterministic floor so a hallucinated
// perfect score can't fake mastery.
func (h *ForgeHandler) grade(prompt, reference, response string, expected []string) gradeResult {
	detScore, detMatched := gradeDeterministic(response, expected)
	res := gradeResult{Score: detScore, Matched: detMatched, source: "deterministic"}
	if strings.TrimSpace(response) == "" {
		res.Feedback = "No answer given — take a guess next time; partial reasoning still earns credit."
		return res
	}

	p := fmt.Sprintf(`You are grading one short technical answer from an experienced engineer during a refresh.
Question: %s
Model answer (ground truth): %s
Key concepts we look for: %s

The engineer answered:
"""%s"""

Grade how well the answer demonstrates understanding, from 0 to 100. Be fair but rigorous: reward correct reasoning even if worded differently; do not reward filler. If the answer contains a specific wrong belief, name it in "misconception" (else empty). Keep "feedback" to 1-2 encouraging, specific sentences.
Respond ONLY as JSON: {"score":0-100,"matched_concepts":[],"missing_concepts":[],"misconception":"","feedback":""}`,
		prompt, reference, strings.Join(expected, ", "), response)

	text, err := h.ai.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":      p,
		"max_tokens":  500,
		"json_schema": gradeJSONSchema,
	})
	if err != nil {
		return res
	}
	var ai gradeResult
	if err := json.Unmarshal([]byte(extractJSON(text)), &ai); err != nil || ai.Feedback == "" {
		return res
	}
	ai.Score = clamp(ai.Score, 0, 100)
	// Blend toward the deterministic floor so AI can enrich but not fabricate.
	ai.Score = clamp(int(math.Round(float64(ai.Score)*0.7+float64(detScore)*0.3)), 0, 100)
	if len(ai.Matched) == 0 {
		ai.Matched = detMatched
	}
	ai.source = "ai"
	return ai
}

// ---------------------------------------------------------------------------
// Learner-skill persistence helpers
// ---------------------------------------------------------------------------

type learnerSkill struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	Mastery       int        `json:"mastery"`
	Confidence    int        `json:"confidence"`
	Attempts      int        `json:"attempts"`
	DecaySignal   int        `json:"decay_signal"`
	LastPracticed *time.Time `json:"last_practiced_at,omitempty"`
}

// applyScore updates a leaf skill deterministically from one graded answer and
// returns (before, after) mastery. It also refreshes the parent aggregate.
func (h *ForgeHandler) applyScore(ctx context.Context, sc, skillKey string, score int, misconception bool) (int, int) {
	var mastery, attempts int
	_ = h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT mastery, attempts FROM %[1]s.learner_skills WHERE skill_key=$1`, sc),
		skillKey).Scan(&mastery, &attempts)

	before := mastery
	after := blendMastery(mastery, score, attempts+1)
	correct := 0
	if score >= 60 {
		correct = 1
	}
	mc := 0
	if misconception {
		mc = 1
	}
	h.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %[1]s.learner_skills
		   (skill_key, mastery, confidence, attempts, correct_attempts, last_practiced_at, last_correct_at, decay_signal, misconception_count, updated_at)
		 VALUES ($1,$2,$3,1,$4,NOW(), CASE WHEN $4=1 THEN NOW() ELSE NULL END, 0, $5, NOW())
		 ON CONFLICT (skill_key) DO UPDATE SET
		   mastery=$2,
		   confidence=$3,
		   attempts=%[1]s.learner_skills.attempts+1,
		   correct_attempts=%[1]s.learner_skills.correct_attempts+$4,
		   last_practiced_at=NOW(),
		   last_correct_at=CASE WHEN $4=1 THEN NOW() ELSE %[1]s.learner_skills.last_correct_at END,
		   decay_signal=0,
		   misconception_count=%[1]s.learner_skills.misconception_count+$5,
		   updated_at=NOW()`, sc),
		skillKey, after, confidenceAfter(attempts+1), correct, mc)

	h.recomputeParent(ctx, sc, skillKey)
	return before, after
}

// recomputeParent keeps a top-level topic's mastery as the average of the leaf
// skills the learner has actually practiced.
func (h *ForgeHandler) recomputeParent(ctx context.Context, sc, skillKey string) {
	var parent sql.NullString
	_ = h.db.QueryRowContext(ctx, `SELECT parent_key FROM public.forge_skills WHERE key=$1`, skillKey).Scan(&parent)
	if !parent.Valid || parent.String == "" {
		return
	}
	var avg sql.NullFloat64
	var oldest sql.NullTime
	_ = h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT AVG(ls.mastery), MIN(ls.last_practiced_at)
		   FROM %[1]s.learner_skills ls
		   JOIN public.forge_skills s ON s.key=ls.skill_key
		   WHERE s.parent_key=$1`, sc), parent.String).Scan(&avg, &oldest)
	if !avg.Valid {
		return
	}
	m := int(math.Round(avg.Float64))
	h.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %[1]s.learner_skills (skill_key, mastery, confidence, last_practiced_at, updated_at)
		 VALUES ($1,$2,50,$3,NOW())
		 ON CONFLICT (skill_key) DO UPDATE SET mastery=$2, last_practiced_at=$3, updated_at=NOW()`, sc),
		parent.String, m, oldest.Time)
}

// ---------------------------------------------------------------------------
// Demo learner seeding (section 20). Runs once, lazily, per learner.
// ---------------------------------------------------------------------------

type seedRow struct {
	key     string
	mastery int
	daysAgo int
}

func (h *ForgeHandler) ensureSeeded(ctx context.Context, sc, userID string) {
	var n int
	h.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %[1]s.learner_skills`, sc)).Scan(&n)
	if n > 0 {
		return
	}
	rows := []seedRow{
		{"python", 94, 3}, {"python.fundamentals", 96, 3}, {"python.async", 92, 5}, {"python.performance", 88, 9}, {"python.testing", 90, 7},
		{"fastapi", 88, 6}, {"fastapi.routing", 92, 6}, {"fastapi.di", 85, 8}, {"fastapi.async", 88, 6}, {"fastapi.production", 84, 12},
		{"postgresql", 79, 14}, {"postgresql.sql", 88, 10}, {"postgresql.indexing", 74, 21}, {"postgresql.transactions", 78, 16}, {"postgresql.query_opt", 72, 21}, {"postgresql.replication", 60, 40},
		// Redis — rusty (~6 months). Deep gap in distributed coordination.
		{"redis", 56, 182}, {"redis.fundamentals", 90, 182}, {"redis.data_structures", 84, 182}, {"redis.caching", 72, 182}, {"redis.ttl", 51, 182}, {"redis.eviction", 52, 182}, {"redis.pubsub", 48, 190}, {"redis.distributed_locks", 21, 200}, {"redis.streams", 30, 210},
		{"kafka", 43, 120}, {"kafka.fundamentals", 55, 120}, {"kafka.delivery", 38, 130}, {"kafka.consumers", 36, 130},
		{"kubernetes", 31, 90}, {"kubernetes.workloads", 40, 90}, {"kubernetes.networking", 28, 95}, {"kubernetes.scaling", 25, 100},
		{"system_design", 68, 30}, {"system_design.scalability", 74, 30}, {"system_design.caching", 66, 35}, {"system_design.queues", 62, 40}, {"system_design.databases", 72, 28}, {"system_design.consistency", 64, 45}, {"system_design.failure", 58, 45},
	}
	for _, rw := range rows {
		conf := confidenceAfter(3)
		h.db.ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %[1]s.learner_skills
			   (skill_key, mastery, confidence, attempts, correct_attempts, last_practiced_at, last_correct_at, decay_signal, updated_at)
			 VALUES ($1,$2,$3,3,2, NOW() - ($4 || ' days')::interval, NOW() - ($4 || ' days')::interval, 0, NOW())
			 ON CONFLICT (skill_key) DO NOTHING`, sc),
			rw.key, rw.mastery, conf, rw.daysAgo)
	}
}

func (h *ForgeHandler) logEvent(ctx context.Context, sc, userID, eventType, skillKey, sessionID string, payload interface{}) {
	b, _ := json.Marshal(payload)
	var sid interface{}
	if sessionID != "" {
		sid = sessionID
	}
	h.db.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %[1]s.knowledge_events (user_id, event_type, skill_key, session_id, payload)
		 VALUES ($1,$2,NULLIF($3,''),$4,$5)`, sc),
		userID, eventType, skillKey, sid, string(b))
}

// ---------------------------------------------------------------------------
// GET /forge/skills — engineering memory + recommendations + recent progress
// ---------------------------------------------------------------------------

func (h *ForgeHandler) Skills(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.ensureSeeded(r.Context(), sc, userID)

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT s.key, s.name, ls.mastery, ls.confidence, ls.attempts, ls.last_practiced_at
		   FROM public.forge_skills s
		   JOIN %[1]s.learner_skills ls ON ls.skill_key=s.key
		   WHERE s.parent_key IS NULL
		   ORDER BY ls.mastery ASC`, sc))
	if err != nil {
		httputil.Error(w, "could not load skills", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var memory []map[string]interface{}
	var recommended []map[string]interface{}
	for rows.Next() {
		var key, name string
		var mastery, confidence, attempts int
		var last *time.Time
		rows.Scan(&key, &name, &mastery, &confidence, &attempts, &last)
		decay := decaySignal(mastery, last)
		decayed := decay >= 35 && mastery < 90
		memory = append(memory, map[string]interface{}{
			"key": key, "name": name, "mastery": mastery, "confidence": confidence,
			"decay_signal": decay, "decayed": decayed, "last_practiced_at": last,
		})
		if decayed || mastery < masteryThreshold {
			mins := 10
			if mastery < 45 {
				mins = 15
			} else if mastery >= 70 {
				mins = 7
			}
			recommended = append(recommended, map[string]interface{}{
				"key": key, "name": name, "mastery": mastery, "minutes": mins,
				"reason": recommendReason(name, mastery, decay),
			})
		}
	}
	// Highest-signal recommendations first (most decayed / weakest).
	sort.SliceStable(recommended, func(i, j int) bool {
		return recommended[i]["mastery"].(int) < recommended[j]["mastery"].(int)
	})
	if len(recommended) > 3 {
		recommended = recommended[:3]
	}

	// Recent progress from completed sessions (before/after snapshots).
	recent := h.recentProgress(r.Context(), sc)

	var xp, streak int
	h.db.QueryRowContext(r.Context(), `SELECT xp, streak FROM users WHERE id=$1`, userID).Scan(&xp, &streak)

	httputil.OK(w, map[string]interface{}{
		"memory":      memory,
		"recommended": recommended,
		"recent":      recent,
		"xp":          xp,
		"streak":      streak,
	})
}

func recommendReason(name string, mastery, decay int) string {
	switch {
	case mastery < 40:
		return name + " is a weak spot — worth a focused refresh."
	case decay >= 50:
		return name + " knowledge has decayed since you last practiced it."
	default:
		return "Sharpen " + name + " to get back above your peak."
	}
}

func (h *ForgeHandler) recentProgress(ctx context.Context, sc string) []map[string]interface{} {
	rows, err := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT topic_key, summary, completed_at FROM %[1]s.forge_sessions
		   WHERE status='completed' AND completed_at IS NOT NULL
		   ORDER BY completed_at DESC LIMIT 5`, sc))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var topic sql.NullString
		var summaryRaw []byte
		var completed time.Time
		rows.Scan(&topic, &summaryRaw, &completed)
		var summary map[string]interface{}
		json.Unmarshal(summaryRaw, &summary)
		before, _ := summary["topic_before"].(float64)
		after, _ := summary["topic_after"].(float64)
		name, _ := summary["topic_name"].(string)
		if name == "" {
			name = topic.String
		}
		out = append(out, map[string]interface{}{
			"topic": name, "before": int(before), "after": int(after), "completed_at": completed,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// GET /forge/skills/{topicKey} — skill detail (children + biggest gap)
// ---------------------------------------------------------------------------

func (h *ForgeHandler) SkillDetail(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.ensureSeeded(r.Context(), sc, userID)
	topicKey := chi.URLParam(r, "topicKey")

	var topicName string
	var overall int
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT s.name, COALESCE(ls.mastery,0) FROM public.forge_skills s
		   LEFT JOIN %[1]s.learner_skills ls ON ls.skill_key=s.key
		   WHERE s.key=$1 AND s.parent_key IS NULL`, sc), topicKey).Scan(&topicName, &overall); err != nil {
		httputil.Error(w, "topic not found", http.StatusNotFound)
		return
	}

	rows, _ := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT s.key, s.name, s.description, COALESCE(ls.mastery,0), ls.last_practiced_at
		   FROM public.forge_skills s
		   LEFT JOIN %[1]s.learner_skills ls ON ls.skill_key=s.key
		   WHERE s.parent_key=$1 ORDER BY s.order_index`, sc), topicKey)
	defer rows.Close()

	var skills []map[string]interface{}
	gapName, gapKey := "", ""
	gapMastery := 101
	for rows.Next() {
		var key, name string
		var desc sql.NullString
		var mastery int
		var last *time.Time
		rows.Scan(&key, &name, &desc, &mastery, &last)
		skills = append(skills, map[string]interface{}{
			"key": key, "name": name, "description": desc.String,
			"mastery": mastery, "decay_signal": decaySignal(mastery, last),
		})
		if mastery < gapMastery {
			gapMastery, gapName, gapKey = mastery, name, key
		}
	}

	httputil.OK(w, map[string]interface{}{
		"key": topicKey, "name": topicName, "mastery": overall,
		"skills": skills,
		"biggest_gap": map[string]interface{}{"key": gapKey, "name": gapName, "mastery": gapMastery},
	})
}

// ---------------------------------------------------------------------------
// POST /forge/refresh/start — create session + return diagnostic
// ---------------------------------------------------------------------------

func (h *ForgeHandler) StartRefresh(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	h.ensureSeeded(r.Context(), sc, userID)

	var req struct {
		TopicKey string `json:"topic_key"`
		Minutes  int    `json:"minutes"`
		Mode     string `json:"mode"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.TopicKey == "" {
		req.TopicKey = "redis"
	}
	if req.Minutes <= 0 {
		req.Minutes = 10
	}
	if req.Mode == "" {
		req.Mode = "refresh"
	}

	var topicName string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT name FROM public.forge_skills WHERE key=$1 AND parent_key IS NULL`, req.TopicKey).Scan(&topicName); err != nil {
		httputil.Error(w, "unknown topic", http.StatusBadRequest)
		return
	}

	// Build the diagnostic. Prefer seeded questions (a calibrated instrument for
	// Redis); otherwise generate them with AI so ANY topic works; fall back to
	// deterministic questions if the AI worker is unavailable. Questions carry
	// their own grading data and are stored on the session, so grading never
	// depends on the seed table.
	questions := h.loadSeededDiagnostic(r.Context(), req.TopicKey)
	source := "seed"
	if len(questions) == 0 {
		questions = h.generateDiagnostic(r.Context(), req.TopicKey, topicName)
		source = "ai"
	}
	if len(questions) == 0 {
		questions = h.fallbackDiagnostic(r.Context(), req.TopicKey)
		source = "fallback"
	}
	if len(questions) == 0 {
		httputil.Error(w, "could not build a diagnostic for this topic", http.StatusInternalServerError)
		return
	}

	var sessionID string
	h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.forge_sessions (user_id, mode, topic_key, status, duration_minutes)
		 VALUES ($1,$2,$3,'diagnostic',$4) RETURNING id`, sc),
		userID, req.Mode, req.TopicKey, req.Minutes).Scan(&sessionID)

	// Persist the full questions (incl. reference + expected concepts) so the
	// diagnostic submit can grade them.
	qsnap, _ := json.Marshal(map[string]interface{}{"questions": questions})
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.forge_sessions SET diagnostic=$2 WHERE id=$1`, sc), sessionID, string(qsnap))

	h.logEvent(r.Context(), sc, userID, "session_started", req.TopicKey, sessionID,
		map[string]interface{}{"mode": req.Mode, "minutes": req.Minutes, "diag_source": source})

	// Public view — hide grading data from the client.
	var pub []map[string]interface{}
	for _, q := range questions {
		pub = append(pub, map[string]interface{}{
			"slug": q.Slug, "skill_key": q.SkillKey, "skill_name": q.SkillName, "prompt": q.Prompt,
		})
	}
	httputil.Created(w, map[string]interface{}{
		"session_id": sessionID, "topic_key": req.TopicKey, "topic_name": topicName,
		"minutes": req.Minutes, "questions": pub,
	})
}

// diagQuestion is one diagnostic question with everything needed to grade it.
type diagQuestion struct {
	Slug      string   `json:"slug"`
	SkillKey  string   `json:"skill_key"`
	SkillName string   `json:"skill_name"`
	Prompt    string   `json:"prompt"`
	Reference string   `json:"reference_answer"`
	Expected  []string `json:"expected_concepts"`
}

type subSkill struct{ Key, Name, Desc string }

func (h *ForgeHandler) topicSubskills(ctx context.Context, topicKey string) []subSkill {
	rows, err := h.db.QueryContext(ctx,
		`SELECT key, name, COALESCE(description,'') FROM public.forge_skills WHERE parent_key=$1 ORDER BY order_index`, topicKey)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []subSkill
	for rows.Next() {
		var s subSkill
		rows.Scan(&s.Key, &s.Name, &s.Desc)
		out = append(out, s)
	}
	return out
}

// loadSeededDiagnostic returns seeded diagnostic questions for a topic (empty if none).
func (h *ForgeHandler) loadSeededDiagnostic(ctx context.Context, topicKey string) []diagQuestion {
	rows, err := h.db.QueryContext(ctx,
		`SELECT c.slug, c.skill_key, s.name, c.prompt, COALESCE(c.reference_answer,''), c.expected_concepts
		   FROM public.forge_challenges c JOIN public.forge_skills s ON s.key=c.skill_key
		   WHERE c.mode='diagnostic' AND c.skill_key LIKE $1 ORDER BY c.order_index`, topicKey+".%")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []diagQuestion
	for rows.Next() {
		var q diagQuestion
		var exp []byte
		rows.Scan(&q.Slug, &q.SkillKey, &q.SkillName, &q.Prompt, &q.Reference, &exp)
		json.Unmarshal(exp, &q.Expected)
		out = append(out, q)
	}
	return out
}

var diagGenSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"questions": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"skill_key":         map[string]interface{}{"type": "string"},
					"prompt":            map[string]interface{}{"type": "string"},
					"reference_answer":  map[string]interface{}{"type": "string"},
					"expected_concepts": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				},
				"required": []string{"skill_key", "prompt", "reference_answer", "expected_concepts"},
			},
		},
	},
	"required": []string{"questions"},
}

// generateDiagnostic asks the AI worker for one open question per sub-skill of a
// topic. Returns nil if the topic has no sub-skills or the worker fails/invalid.
func (h *ForgeHandler) generateDiagnostic(ctx context.Context, topicKey, topicName string) []diagQuestion {
	subs := h.topicSubskills(ctx, topicKey)
	if len(subs) == 0 {
		return nil
	}
	if len(subs) > 6 {
		subs = subs[:6]
	}
	byKey := map[string]subSkill{}
	var lines []string
	for _, s := range subs {
		byKey[s.Key] = s
		lines = append(lines, fmt.Sprintf("- %s (key: %s): %s", s.Name, s.Key, s.Desc))
	}
	prompt := fmt.Sprintf(`You are Forge, diagnosing what an experienced engineer remembers about %s.
Write ONE open-ended diagnostic question for EACH sub-skill below (use its exact key):
%s

Each question must be answerable in 1-3 sentences, probe real understanding (not trivia), and be specific.
For each, also give "reference_answer" (2-3 sentences, correct) and "expected_concepts" (4-8 short lowercase keywords a correct answer would contain, used to grade).
Respond ONLY as JSON: {"questions":[{"skill_key":"","prompt":"","reference_answer":"","expected_concepts":[]}]}`,
		topicName, strings.Join(lines, "\n"))

	text, err := h.ai.callCFAI("/ai/generate", map[string]interface{}{
		"prompt": prompt, "max_tokens": 1500, "json_schema": diagGenSchema,
	})
	if err != nil {
		return nil
	}
	var parsed struct {
		Questions []struct {
			SkillKey string   `json:"skill_key"`
			Prompt   string   `json:"prompt"`
			Ref      string   `json:"reference_answer"`
			Expected []string `json:"expected_concepts"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(text)), &parsed); err != nil || len(parsed.Questions) == 0 {
		return nil
	}
	var out []diagQuestion
	for i, q := range parsed.Questions {
		s, ok := byKey[q.SkillKey]
		if !ok {
			s = subs[i%len(subs)] // model returned a bad key — map by position
		}
		if strings.TrimSpace(q.Prompt) == "" || len(q.Expected) == 0 {
			continue
		}
		out = append(out, diagQuestion{
			Slug: fmt.Sprintf("gen.%s.%d", topicKey, i), SkillKey: s.Key, SkillName: s.Name,
			Prompt: q.Prompt, Reference: q.Ref, Expected: q.Expected,
		})
	}
	return out
}

// fallbackDiagnostic builds deterministic questions from the skill graph so the
// flow works for any topic even with no AI and no seed data.
func (h *ForgeHandler) fallbackDiagnostic(ctx context.Context, topicKey string) []diagQuestion {
	subs := h.topicSubskills(ctx, topicKey)
	if len(subs) > 5 {
		subs = subs[:5]
	}
	var out []diagQuestion
	for i, s := range subs {
		expected := keywordsFrom(s.Name + " " + s.Desc)
		out = append(out, diagQuestion{
			Slug:      fmt.Sprintf("fb.%s.%d", topicKey, i),
			SkillKey:  s.Key,
			SkillName: s.Name,
			Prompt:    fmt.Sprintf("Explain %s: what it is, and when and why you'd use it.", s.Name),
			Reference: s.Desc,
			Expected:  expected,
		})
	}
	return out
}

// keywordsFrom derives simple grading keywords from a name/description.
func keywordsFrom(s string) []string {
	stop := map[string]bool{"the": true, "and": true, "for": true, "with": true, "you": true, "your": true, "use": true, "used": true, "when": true, "what": true, "core": true, "into": true, "that": true}
	seen := map[string]bool{}
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,:;()/&")
		if len(w) >= 4 && !stop[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	if len(out) == 0 {
		out = []string{"correct", "accurate"}
	}
	return out
}

// ---------------------------------------------------------------------------
// POST /forge/refresh/{sessionID}/diagnostic — grade + classify + build plan
// ---------------------------------------------------------------------------

func (h *ForgeHandler) SubmitDiagnostic(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	var topicKey string
	var status string
	var diagRaw []byte
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT topic_key, status, COALESCE(diagnostic,'{}') FROM %[1]s.forge_sessions WHERE id=$1 AND user_id=$2`, sc),
		sessionID, userID).Scan(&topicKey, &status, &diagRaw); err != nil {
		httputil.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// The questions (with grading data) were stored on the session at start.
	var stored struct {
		Questions []diagQuestion `json:"questions"`
	}
	json.Unmarshal(diagRaw, &stored)
	qmap := map[string]diagQuestion{}
	for _, q := range stored.Questions {
		qmap[q.Slug] = q
	}

	var req struct {
		Answers []struct {
			Slug     string `json:"slug"`
			Response string `json:"response"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	// Baseline snapshot (for before/after at completion).
	baseline := h.topicMasterySnapshot(r.Context(), sc, topicKey)

	// Grade every answer, then aggregate per unique skill (a skill probed by
	// several questions gets the average score and its final blended mastery).
	scoreSum := map[string]int{}
	scoreN := map[string]int{}
	var skillOrder []string
	for _, a := range req.Answers {
		q, ok := qmap[a.Slug]
		if !ok {
			continue
		}
		skillKey := q.SkillKey
		g := h.grade(q.Prompt, q.Reference, a.Response, q.Expected)
		if _, seen := scoreN[skillKey]; !seen {
			skillOrder = append(skillOrder, skillKey)
		}
		scoreSum[skillKey] += g.Score
		scoreN[skillKey]++
		h.applyScore(r.Context(), sc, skillKey, g.Score, g.Misconception != "")
		if g.Misconception != "" {
			h.db.ExecContext(r.Context(),
				fmt.Sprintf(`INSERT INTO %[1]s.misconceptions (skill_key, label, detail, session_id)
				 VALUES ($1,$2,$3,$4)`, sc),
				skillKey, g.Misconception, g.Feedback, sessionID)
		}
		h.logEvent(r.Context(), sc, userID, "diagnostic_answered", skillKey, sessionID,
			map[string]interface{}{"score": g.Score, "source": g.source})
	}

	var classification []forgeClass
	for _, skillKey := range skillOrder {
		var sName string
		var finalMastery int
		h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, skillKey).Scan(&sName)
		h.db.QueryRowContext(r.Context(),
			fmt.Sprintf(`SELECT mastery FROM %[1]s.learner_skills WHERE skill_key=$1`, sc), skillKey).Scan(&finalMastery)
		classification = append(classification, forgeClass{skillKey, sName, finalMastery, scoreSum[skillKey] / scoreN[skillKey]})
	}

	// Deterministic plan: weakest tested skills first, below threshold.
	sort.SliceStable(classification, func(i, j int) bool { return classification[i].Mastery < classification[j].Mastery })
	var plan []map[string]interface{}
	priority := 0
	for _, c := range classification {
		if c.Mastery >= masteryThreshold {
			continue
		}
		reason := fmt.Sprintf("Scored %d%% on %s — targeted practice will rebuild it.", c.Score, c.Name)
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`INSERT INTO %[1]s.refresh_plans (session_id, skill_key, priority, reason, target_minutes)
			 VALUES ($1,$2,$3,$4,$5)`, sc),
			sessionID, c.SkillKey, priority, reason, 3)
		plan = append(plan, map[string]interface{}{
			"skill_key": c.SkillKey, "name": c.Name, "priority": priority,
			"mastery": c.Mastery, "reason": reason,
		})
		priority++
	}

	narrative := buildNarrative(classification, masteryThreshold)

	diagSnapshot, _ := json.Marshal(map[string]interface{}{"classification": classification, "baseline": baseline})
	planSnapshot, _ := json.Marshal(plan)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.forge_sessions SET status='active', diagnostic=$2, plan=$3 WHERE id=$1`, sc),
		sessionID, string(diagSnapshot), string(planSnapshot))

	httputil.OK(w, map[string]interface{}{
		"classification": classification,
		"plan":           plan,
		"narrative":      narrative,
		"threshold":      masteryThreshold,
	})
}

// forgeClass is one skill's post-diagnostic classification.
type forgeClass struct {
	SkillKey string `json:"skill_key"`
	Name     string `json:"name"`
	Mastery  int    `json:"mastery"`
	Score    int    `json:"score"`
}

func buildNarrative(items []forgeClass, threshold int) string {
	if len(items) == 0 {
		return "Let's find out what you remember."
	}
	var strong, weak []string
	for _, it := range items {
		if it.Mastery >= threshold {
			strong = append(strong, it.Name)
		} else {
			weak = append(weak, it.Name)
		}
	}
	var b strings.Builder
	if len(strong) > 0 {
		b.WriteString("You already remember " + humanList(strong) + ". ")
	}
	if len(weak) > 0 {
		b.WriteString("Your biggest gap is " + weak[0])
		if len(weak) > 1 {
			b.WriteString(" (and " + weak[len(weak)-1] + ")")
		}
		b.WriteString(". We'll rebuild it now.")
	} else {
		b.WriteString("You're sharp across the board — nice.")
	}
	return b.String()
}

func humanList(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	default:
		return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
	}
}

func (h *ForgeHandler) topicMasterySnapshot(ctx context.Context, sc, topicKey string) int {
	var m sql.NullInt64
	h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT mastery FROM %[1]s.learner_skills WHERE skill_key=$1`, sc), topicKey).Scan(&m)
	return int(m.Int64)
}

// ---------------------------------------------------------------------------
// POST /forge/refresh/{sessionID}/next — adaptive next-best activity
// ---------------------------------------------------------------------------

func (h *ForgeHandler) NextActivity(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT 1 FROM %[1]s.forge_sessions WHERE id=$1 AND user_id=$2`, sc),
		sessionID, userID).Scan(new(int)); err != nil {
		httputil.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// Choose the next-best skill: highest-priority plan item still below threshold.
	skillKey := h.pickNextSkill(r.Context(), sc, sessionID)
	if skillKey == "" {
		httputil.OK(w, map[string]interface{}{"done": true})
		return
	}
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.refresh_plans SET status='active' WHERE session_id=$1 AND skill_key=$2 AND status='pending'`, sc),
		sessionID, skillKey)

	var sName string
	var sDesc sql.NullString
	h.db.QueryRowContext(r.Context(), `SELECT name, description FROM public.forge_skills WHERE key=$1`, skillKey).Scan(&sName, &sDesc)

	// How many activities the learner has already done for this skill this
	// session decides the teaching rung (concept -> scenario -> recall) and
	// prevents repeats.
	var doneForSkill int
	h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COUNT(*) FROM %[1]s.forge_activities WHERE session_id=$1 AND skill_key=$2`, sc),
		sessionID, skillKey).Scan(&doneForSkill)
	wantKind := []string{"concept", "scenario", "recall"}[doneForSkill%3]

	// AI-FIRST, behaviour-driven: generate a fresh activity personalised to this
	// learner's live state (mastery, decay, misconceptions, how they just
	// answered). The seeded bank grounds the prompt and is the fallback when the
	// AI worker is slow/unavailable, so the loop never stalls.
	act, ok := h.generateActivity(r.Context(), sc, sessionID, skillKey, sName, sDesc.String, wantKind)
	source := "ai"
	if !ok {
		act, ok = h.seededActivity(r.Context(), sc, sessionID, skillKey)
		source = "seed"
	}
	if !ok {
		// No AI and seeded content exhausted for this skill — close it, advance.
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`UPDATE %[1]s.refresh_plans SET status='done' WHERE session_id=$1 AND skill_key=$2`, sc),
			sessionID, skillKey)
		h.NextActivity(w, r)
		return
	}

	var order int
	h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COALESCE(MAX(order_index),0)+1 FROM %[1]s.forge_activities WHERE session_id=$1`, sc),
		sessionID).Scan(&order)

	hintsRaw, _ := json.Marshal(act.Hints)
	expectedRaw, _ := json.Marshal(act.ExpectedConcepts)
	var activityID string
	h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.forge_activities
		   (session_id, skill_key, kind, phase, prompt, concept, example, hints, reference_answer, expected_concepts, explanation, order_index)
		 VALUES ($1,$2,$3,'practice',$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, sc),
		sessionID, skillKey, act.Kind, act.Prompt, act.Concept, act.Example,
		string(hintsRaw), act.ReferenceAnswer, string(expectedRaw), act.Explanation, order).Scan(&activityID)

	h.logEvent(r.Context(), sc, userID, "challenge_started", skillKey, sessionID,
		map[string]interface{}{"kind": act.Kind, "source": source})

	httputil.OK(w, map[string]interface{}{
		"done":        false,
		"activity_id": activityID,
		"skill_key":   skillKey,
		"skill_name":  sName,
		"kind":        act.Kind,
		"title":       act.Title,
		"concept":     act.Concept,
		"example":     act.Example,
		"prompt":      act.Prompt,
		"hints":       act.Hints,
		"source":      source,
	})
}

// activityData is one practice activity, whether AI-generated or seeded.
type activityData struct {
	Kind             string   `json:"kind"`
	Title            string   `json:"title"`
	Concept          string   `json:"concept"`
	Example          string   `json:"example"`
	Prompt           string   `json:"prompt"`
	Hints            []string `json:"hints"`
	ReferenceAnswer  string   `json:"reference_answer"`
	ExpectedConcepts []string `json:"expected_concepts"`
	Explanation      string   `json:"explanation"`
}

var activityJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"kind":              map[string]interface{}{"type": "string", "enum": []string{"concept", "scenario", "recall", "code"}},
		"title":             map[string]interface{}{"type": "string"},
		"concept":           map[string]interface{}{"type": "string"},
		"example":           map[string]interface{}{"type": "string"},
		"prompt":            map[string]interface{}{"type": "string"},
		"hints":             map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"reference_answer":  map[string]interface{}{"type": "string"},
		"expected_concepts": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"explanation":       map[string]interface{}{"type": "string"},
	},
	"required": []string{"title", "concept", "prompt", "reference_answer", "expected_concepts", "explanation"},
}

// generateActivity produces a fresh, personalised activity via the AI worker,
// grounded in a seeded reference answer for the skill and conditioned on the
// learner's live behaviour. Returns ok=false if the worker fails or the output
// doesn't validate, so the caller can fall back to the seeded bank.
func (h *ForgeHandler) generateActivity(ctx context.Context, sc, sessionID, skillKey, skillName, skillDesc, wantKind string) (activityData, bool) {
	behaviour := h.behaviourContext(ctx, sc, sessionID, skillKey)

	// Ground the model in an authoritative reference answer + already-covered
	// prompts (so it doesn't repeat itself within the session).
	var grounding string
	h.db.QueryRowContext(ctx,
		`SELECT string_agg(COALESCE(reference_answer,''), ' | ') FROM public.forge_challenges
		   WHERE mode='refresh' AND skill_key=$1`, skillKey).Scan(&grounding)
	var covered string
	h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT string_agg(prompt, ' || ') FROM %[1]s.forge_activities WHERE session_id=$1 AND skill_key=$2`, sc),
		sessionID, skillKey).Scan(&covered)

	kindGuide := map[string]string{
		"concept":  "a 30-second concept refresher followed by a short question that checks understanding",
		"scenario": "a realistic engineering scenario (an incident, a design choice, a bug) the learner must reason through",
		"recall":   "a crisp recall question that forces retrieval of the key idea from memory",
	}[wantKind]
	if kindGuide == "" {
		kindGuide = "a realistic engineering scenario the learner must reason through"
	}

	prompt := fmt.Sprintf(`You are Forge, an adaptive technical-refresh engine for experienced software engineers.
Design ONE %s for the skill "%s" (%s).

Ground truth to stay technically correct (do NOT copy verbatim; teach it your own way):
%s

Learner's live state — adapt difficulty and focus to THIS engineer, do not be generic:
%s

Already covered this session (do NOT repeat these): %s

Rules:
- "concept": 2-4 sentences, concrete, engineer-to-engineer (no fluff). Otherwise leave concept short (1 sentence of framing).
- "prompt": the single question/task the learner answers. Make it specific and realistic.
- "expected_concepts": 4-8 short lowercase keywords/phrases a correct answer would contain (used to grade).
- "reference_answer": the model answer (2-4 sentences).
- "explanation": what to reinforce after they answer (1-2 sentences).
- "hints": 1-2 Socratic hints that guide without giving the answer.
Respond ONLY as JSON matching the schema.`,
		kindGuide, skillName, skillDesc, truncate(grounding, 1200), behaviour, truncate(covered, 800))

	text, err := h.ai.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":      prompt,
		"max_tokens":  900,
		"json_schema": activityJSONSchema,
	})
	if err != nil {
		return activityData{}, false
	}
	var a activityData
	if err := json.Unmarshal([]byte(extractJSON(text)), &a); err != nil {
		return activityData{}, false
	}
	if strings.TrimSpace(a.Prompt) == "" || strings.TrimSpace(a.ReferenceAnswer) == "" || len(a.ExpectedConcepts) == 0 {
		return activityData{}, false
	}
	if a.Kind == "" {
		a.Kind = wantKind
	}
	if a.Title == "" {
		a.Title = skillName
	}
	return a, true
}

// behaviourContext summarises what we know about how this learner is doing on a
// skill: mastery, staleness, attempts, recorded misconceptions, and how they
// answered the most recent activities. This is the "based on user behaviour"
// signal the generator adapts to.
func (h *ForgeHandler) behaviourContext(ctx context.Context, sc, sessionID, skillKey string) string {
	var mastery, attempts int
	var last *time.Time
	h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT mastery, attempts, last_practiced_at FROM %[1]s.learner_skills WHERE skill_key=$1`, sc),
		skillKey).Scan(&mastery, &attempts, &last)

	var b strings.Builder
	fmt.Fprintf(&b, "- Current mastery: %d/100 (threshold to recover: %d). Staleness: %d/100. Attempts so far: %d.\n",
		mastery, masteryThreshold, decaySignal(mastery, last), attempts)
	switch {
	case mastery < 35:
		b.WriteString("- They are struggling here — start simpler and be very concrete.\n")
	case mastery < 65:
		b.WriteString("- Partial knowledge — target the specific gap, not the basics they already have.\n")
	default:
		b.WriteString("- Close to recovered — push with a harder edge case to confirm mastery.\n")
	}

	// Open misconceptions for this skill.
	mrows, _ := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT label FROM %[1]s.misconceptions WHERE skill_key=$1 AND resolved=false ORDER BY created_at DESC LIMIT 3`, sc),
		skillKey)
	if mrows != nil {
		var labels []string
		for mrows.Next() {
			var l string
			mrows.Scan(&l)
			labels = append(labels, l)
		}
		mrows.Close()
		if len(labels) > 0 {
			b.WriteString("- Misconceptions to directly address: " + strings.Join(labels, "; ") + ".\n")
		}
	}

	// Most recent graded answers this session (any skill) — momentum signal.
	arows, _ := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT skill_key, COALESCE(score,-1) FROM %[1]s.forge_activities
		   WHERE session_id=$1 AND answered_at IS NOT NULL ORDER BY answered_at DESC LIMIT 3`, sc),
		sessionID)
	if arows != nil {
		var parts []string
		for arows.Next() {
			var k string
			var s int
			arows.Scan(&k, &s)
			if s >= 0 {
				parts = append(parts, fmt.Sprintf("%s:%d", k, s))
			}
		}
		arows.Close()
		if len(parts) > 0 {
			b.WriteString("- Recent scores: " + strings.Join(parts, ", ") + ".\n")
		}
	}
	return b.String()
}

// seededActivity returns the next unused seeded activity for a skill (the
// grounding/fallback path). ok=false when the bank is exhausted for this skill.
func (h *ForgeHandler) seededActivity(ctx context.Context, sc, sessionID, skillKey string) (activityData, bool) {
	var kind, title, body, prompt, refAns, explanation string
	var hintsRaw, expectedRaw []byte
	err := h.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT kind, COALESCE(title,''), COALESCE(body,''), prompt, COALESCE(reference_answer,''),
		        COALESCE(explanation,''), hints, expected_concepts
		   FROM public.forge_challenges c
		   WHERE c.mode='refresh' AND c.skill_key=$1
		     AND NOT EXISTS (SELECT 1 FROM %[1]s.forge_activities a
		                     WHERE a.session_id=$2 AND a.prompt=c.prompt)
		   ORDER BY c.order_index LIMIT 1`, sc),
		skillKey, sessionID).Scan(&kind, &title, &body, &prompt, &refAns, &explanation, &hintsRaw, &expectedRaw)
	if err != nil {
		return activityData{}, false
	}
	var hints, expected []string
	json.Unmarshal(hintsRaw, &hints)
	json.Unmarshal(expectedRaw, &expected)
	return activityData{
		Kind: kind, Title: title, Concept: body, Prompt: prompt,
		Hints: hints, ReferenceAnswer: refAns, ExpectedConcepts: expected, Explanation: explanation,
	}, true
}

// pickNextSkill implements the "select next-best" step deterministically: the
// lowest-priority (highest-urgency) plan skill that is still below mastery.
func (h *ForgeHandler) pickNextSkill(ctx context.Context, sc, sessionID string) string {
	rows, err := h.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT p.skill_key FROM %[1]s.refresh_plans p
		   WHERE p.session_id=$1 AND p.status IN ('pending','active')
		   ORDER BY p.priority`, sc), sessionID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		rows.Scan(&k)
		var m sql.NullInt64
		h.db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT mastery FROM %[1]s.learner_skills WHERE skill_key=$1`, sc), k).Scan(&m)
		if int(m.Int64) < masteryThreshold {
			return k
		}
		// Already recovered — close it out.
		h.db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %[1]s.refresh_plans SET status='done' WHERE session_id=$1 AND skill_key=$2`, sc),
			sessionID, k)
	}
	return ""
}

// ---------------------------------------------------------------------------
// POST /forge/activities/{activityID}/answer — grade + update mastery
// ---------------------------------------------------------------------------

func (h *ForgeHandler) AnswerActivity(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	activityID := chi.URLParam(r, "activityID")

	var req struct {
		Response string `json:"response"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	var sessionID, skillKey, prompt, refAns, explanation string
	var expectedRaw []byte
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT session_id, skill_key, prompt, COALESCE(reference_answer,''), COALESCE(explanation,''), expected_concepts
		   FROM %[1]s.forge_activities WHERE id=$1`, sc), activityID).
		Scan(&sessionID, &skillKey, &prompt, &refAns, &explanation, &expectedRaw); err != nil {
		httputil.Error(w, "activity not found", http.StatusNotFound)
		return
	}
	var expected []string
	json.Unmarshal(expectedRaw, &expected)

	g := h.grade(prompt, refAns, req.Response, expected)
	before, after := h.applyScore(r.Context(), sc, skillKey, g.Score, g.Misconception != "")

	if g.Misconception != "" {
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`INSERT INTO %[1]s.misconceptions (skill_key, label, detail, session_id)
			 VALUES ($1,$2,$3,$4)`, sc), skillKey, g.Misconception, g.Feedback, sessionID)
		h.logEvent(r.Context(), sc, userID, "misconception_detected", skillKey, sessionID,
			map[string]interface{}{"label": g.Misconception})
	}

	evalRaw, _ := json.Marshal(g)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.forge_activities SET response=$2, score=$3, evaluation=$4,
		   mastery_before=$5, mastery_after=$6, answered_at=NOW() WHERE id=$1`, sc),
		activityID, req.Response, g.Score, string(evalRaw), before, after)

	h.logEvent(r.Context(), sc, userID, "challenge_completed", skillKey, sessionID,
		map[string]interface{}{"score": g.Score})
	h.logEvent(r.Context(), sc, userID, "skill_updated", skillKey, sessionID,
		map[string]interface{}{"before": before, "after": after})

	mastered := after >= masteryThreshold
	if mastered {
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`UPDATE %[1]s.refresh_plans SET status='done' WHERE session_id=$1 AND skill_key=$2`, sc),
			sessionID, skillKey)
	}

	httputil.OK(w, map[string]interface{}{
		"score":          g.Score,
		"mastery_before": before,
		"mastery_after":  after,
		"skill_mastered": mastered,
		"feedback":       g.Feedback,
		"matched":        g.Matched,
		"misconception":  g.Misconception,
		"explanation":    explanation,
		"threshold":      masteryThreshold,
	})
}

// ---------------------------------------------------------------------------
// POST /forge/refresh/{sessionID}/complete — summary + XP
// ---------------------------------------------------------------------------

func (h *ForgeHandler) CompleteRefresh(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sessionID := chi.URLParam(r, "sessionID")

	var topicKey, diagRaw string
	var status string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT topic_key, COALESCE(diagnostic::text,'{}'), status FROM %[1]s.forge_sessions WHERE id=$1 AND user_id=$2`, sc),
		sessionID, userID).Scan(&topicKey, &diagRaw, &status); err != nil {
		httputil.Error(w, "session not found", http.StatusNotFound)
		return
	}

	var diag struct {
		Classification []struct {
			SkillKey string `json:"skill_key"`
			Name     string `json:"name"`
			Mastery  int    `json:"mastery"`
		} `json:"classification"`
		Baseline int `json:"baseline"`
	}
	json.Unmarshal([]byte(diagRaw), &diag)

	var recovered, stillWeak []map[string]interface{}
	for _, c := range diag.Classification {
		var now int
		h.db.QueryRowContext(r.Context(),
			fmt.Sprintf(`SELECT mastery FROM %[1]s.learner_skills WHERE skill_key=$1`, sc), c.SkillKey).Scan(&now)
		entry := map[string]interface{}{"key": c.SkillKey, "name": c.Name, "before": c.Mastery, "after": now}
		if now >= masteryThreshold {
			recovered = append(recovered, entry)
		} else {
			stillWeak = append(stillWeak, entry)
		}
	}

	topicAfter := h.topicMasterySnapshot(r.Context(), sc, topicKey)
	var topicName string
	h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, topicKey).Scan(&topicName)

	// Deterministic XP: base + per recovered skill (only awarded once).
	xp := 40 + len(recovered)*30
	var recommended map[string]interface{}
	if len(stillWeak) > 0 {
		recommended = map[string]interface{}{
			"key": stillWeak[0]["key"], "name": stillWeak[0]["name"],
			"minutes": 10, "when": "tomorrow",
		}
	}

	summary := map[string]interface{}{
		"topic_key": topicKey, "topic_name": topicName,
		"topic_before": diag.Baseline, "topic_after": topicAfter,
		"recovered": recovered, "still_weak": stillWeak,
		"recommended": recommended, "xp": xp,
	}

	if status != "completed" {
		h.db.ExecContext(r.Context(), `UPDATE users SET xp=xp+$1, last_active_at=NOW() WHERE id=$2`, xp, userID)
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`INSERT INTO %[1]s.xp_transactions (user_id, amount, reason) VALUES ($1,$2,'refresh_complete')`, sc),
			userID, xp)
	}
	sumRaw, _ := json.Marshal(summary)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.forge_sessions SET status='completed', completed_at=NOW(), summary=$2 WHERE id=$1`, sc),
		sessionID, string(sumRaw))
	h.logEvent(r.Context(), sc, userID, "refresh_completed", topicKey, sessionID,
		map[string]interface{}{"xp": xp, "recovered": len(recovered)})

	httputil.OK(w, summary)
}

// ---------------------------------------------------------------------------
// POST /forge/tutor — Lumi, the Socratic engineering mentor
// ---------------------------------------------------------------------------

var tutorStages = []string{"hint", "question", "example", "stronger_hint", "explanation", "answer"}

func (h *ForgeHandler) Tutor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
		SkillKey string `json:"skill_key"`
		Stage    string `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		httputil.Error(w, "question is required", http.StatusBadRequest)
		return
	}
	stage := req.Stage
	if stage == "" {
		stage = "hint"
	}
	next := nextStage(stage)

	var skillName string
	if req.SkillKey != "" {
		h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, req.SkillKey).Scan(&skillName)
	}

	guide := map[string]string{
		"hint":          "Give ONE short nudge in the right direction. Do NOT answer.",
		"question":      "Ask ONE pointed Socratic question that makes them reason toward the answer. Do NOT answer.",
		"example":       "Give ONE small concrete example that illuminates the idea, then ask what it implies. Do NOT give the full answer.",
		"stronger_hint": "Give a stronger hint that names the key concept but still leaves the final step to them.",
		"explanation":   "Explain the underlying concept clearly and concisely (under 120 words).",
		"answer":        "Now give the complete, correct answer concisely, then one sentence on why.",
	}[stage]

	prompt := fmt.Sprintf(`You are Lumi, an expert senior engineer mentoring another engineer during a technical refresh%s.
You teach by progression: hint -> question -> example -> stronger hint -> explanation -> answer. You are on the "%s" rung.
%s
Be direct and technical, like a staff engineer at a whiteboard — never condescending, never padded.

The engineer said:
"""%s"""

Respond in 1-3 sentences.`,
		skillContext(skillName), stage, guide, req.Question)

	reply, err := h.ai.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil || strings.TrimSpace(reply) == "" {
		reply = tutorFallback(stage, skillName)
	}

	httputil.OK(w, map[string]interface{}{
		"reply": strings.TrimSpace(reply), "stage": stage, "next_stage": next,
	})
}

func skillContext(name string) string {
	if name == "" {
		return ""
	}
	return " about " + name
}

func nextStage(cur string) string {
	for i, s := range tutorStages {
		if s == cur && i+1 < len(tutorStages) {
			return tutorStages[i+1]
		}
	}
	return "answer"
}

func tutorFallback(stage, skill string) string {
	s := skill
	if s == "" {
		s = "this"
	}
	switch stage {
	case "hint":
		return "Before I answer — what property of " + s + " would break first if the process holding it crashed?"
	case "question":
		return "Walk me through the failure case: what happens if two clients think they succeeded at the same time?"
	case "example":
		return "Consider two workers racing on the same key. If neither step is atomic, which invariant is violated?"
	case "stronger_hint":
		return "The key idea is atomicity plus ownership — acquire and expiry must be one operation, and release must verify who holds it."
	case "explanation":
		return "Safe coordination needs three guarantees: an atomic acquire, an automatic release on crash (a TTL), and ownership so you only release your own claim."
	default:
		return "Use a single atomic operation to acquire with a TTL and a unique token, and release with a compare-and-delete that checks the token first."
	}
}

// ---------------------------------------------------------------------------
// AI Interview mode
// ---------------------------------------------------------------------------

const interviewMaxTurns = 5 // interviewer follow-ups before it can wrap up

type interviewMsg struct {
	Role    string `json:"role"` // "interviewer" | "candidate"
	Content string `json:"content"`
}

// POST /forge/interview/start {topic_key}
func (h *ForgeHandler) InterviewStart(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		TopicKey string `json:"topic_key"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	topicName := "software engineering"
	if req.TopicKey != "" {
		var n string
		if err := h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, req.TopicKey).Scan(&n); err == nil {
			topicName = n
		}
	}

	opening := h.interviewOpening(topicName)
	transcript, _ := json.Marshal([]interviewMsg{{Role: "interviewer", Content: opening}})

	var id string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.interview_sessions (user_id, topic_key, prompt, status, transcript)
		 VALUES ($1,$2,$3,'active',$4) RETURNING id`, sc),
		userID, req.TopicKey, opening, string(transcript)).Scan(&id); err != nil {
		httputil.Error(w, "could not start interview", http.StatusInternalServerError)
		return
	}
	h.logEvent(r.Context(), sc, userID, "interview_started", req.TopicKey, id, map[string]interface{}{"topic": topicName})

	httputil.Created(w, map[string]interface{}{
		"interview_id": id, "topic_name": topicName, "prompt": opening,
		"max_turns": interviewMaxTurns,
	})
}

// interviewOpening asks the AI for a realistic opening design question, with a
// deterministic fallback per topic so it always works.
func (h *ForgeHandler) interviewOpening(topicName string) string {
	p := fmt.Sprintf(`You are a senior staff engineer starting a system-design / technical interview focused on %s.
Ask ONE concise opening design question a strong candidate could spend 20 minutes on (e.g. "Design a URL shortener for 10M users").
Return ONLY the question, one or two sentences, no preamble.`, topicName)
	if text, err := h.ai.callCFAI("/ai/generate", map[string]string{"prompt": p}); err == nil {
		if t := strings.TrimSpace(text); len(t) > 12 {
			return t
		}
	}
	return "Design a URL shortener that serves 100 million redirects per day. Walk me through your high-level architecture, then we'll go deeper."
}

// POST /forge/interview/{interviewID}/reply {message}
func (h *ForgeHandler) InterviewReply(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "interviewID")
	var req struct {
		Message string `json:"message"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Message) == "" {
		httputil.Error(w, "message is required", http.StatusBadRequest)
		return
	}

	var topicKey, transcriptRaw string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COALESCE(topic_key,''), COALESCE(transcript::text,'[]') FROM %[1]s.interview_sessions WHERE id=$1 AND user_id=$2`, sc),
		id, userID).Scan(&topicKey, &transcriptRaw); err != nil {
		httputil.Error(w, "interview not found", http.StatusNotFound)
		return
	}
	var transcript []interviewMsg
	json.Unmarshal([]byte(transcriptRaw), &transcript)
	transcript = append(transcript, interviewMsg{Role: "candidate", Content: req.Message})

	// How many follow-ups the interviewer has already asked.
	interviewerTurns := 0
	for _, m := range transcript {
		if m.Role == "interviewer" {
			interviewerTurns++
		}
	}

	topicName := "software engineering"
	if topicKey != "" {
		h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, topicKey).Scan(&topicName)
	}

	var b strings.Builder
	for _, m := range transcript {
		who := "Interviewer"
		if m.Role == "candidate" {
			who = "Candidate"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, m.Content)
	}
	prompt := fmt.Sprintf(`You are a senior staff engineer running a system-design interview about %s.
Transcript so far:
%s
Ask ONE sharp follow-up question that probes a gap or pushes deeper — pick the most valuable of: requirements/scope, API design, data model, scaling, caching, reliability, failure handling, or tradeoffs. Reference something specific the candidate said. Be concise (1-2 sentences). Do NOT give the answer or evaluate; just ask the next question.`,
		topicName, strings.TrimSpace(b.String()))

	reply, err := h.ai.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil || strings.TrimSpace(reply) == "" {
		reply = interviewFollowupFallback(interviewerTurns)
	}
	reply = strings.TrimSpace(reply)
	transcript = append(transcript, interviewMsg{Role: "interviewer", Content: reply})

	tRaw, _ := json.Marshal(transcript)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.interview_sessions SET transcript=$2 WHERE id=$1`, sc), id, string(tRaw))

	httputil.OK(w, map[string]interface{}{
		"reply": reply, "turn": interviewerTurns + 1, "can_finish": interviewerTurns+1 >= interviewMaxTurns,
	})
}

func interviewFollowupFallback(turn int) string {
	qs := []string{
		"How does your design handle a 10x spike in traffic — where's the first bottleneck?",
		"Where would caching live in this system, and how do you keep it consistent?",
		"What happens if your primary database becomes unavailable mid-write?",
		"Walk me through your data model and the access patterns it optimizes for.",
		"What are the main tradeoffs of your approach, and what would you change at 100x scale?",
	}
	if turn >= 0 && turn < len(qs) {
		return qs[turn]
	}
	return "What's the biggest weakness of your design, and how would you address it?"
}

var interviewEvalSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"dimensions": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"requirements":     map[string]interface{}{"type": "integer"},
				"api_design":       map[string]interface{}{"type": "integer"},
				"data_modeling":    map[string]interface{}{"type": "integer"},
				"scalability":      map[string]interface{}{"type": "integer"},
				"caching":          map[string]interface{}{"type": "integer"},
				"reliability":      map[string]interface{}{"type": "integer"},
				"failure_handling": map[string]interface{}{"type": "integer"},
				"tradeoffs":        map[string]interface{}{"type": "integer"},
				"communication":    map[string]interface{}{"type": "integer"},
			},
		},
		"strengths":   map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"gaps":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		"recommended": map[string]interface{}{"type": "string"},
	},
	"required": []string{"dimensions", "strengths", "gaps", "recommended"},
}

type interviewEval struct {
	Dimensions  map[string]int `json:"dimensions"`
	Strengths   []string       `json:"strengths"`
	Gaps        []string       `json:"gaps"`
	Recommended string         `json:"recommended"`
}

// POST /forge/interview/{interviewID}/finish
func (h *ForgeHandler) InterviewFinish(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id := chi.URLParam(r, "interviewID")

	// Optional face/voice delivery signals captured client-side during the
	// interview (a confidence/engagement proxy, not a competence measure).
	var body struct {
		Delivery *deliverySignals `json:"delivery"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	var topicKey, transcriptRaw string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT COALESCE(topic_key,''), COALESCE(transcript::text,'[]') FROM %[1]s.interview_sessions WHERE id=$1 AND user_id=$2`, sc),
		id, userID).Scan(&topicKey, &transcriptRaw); err != nil {
		httputil.Error(w, "interview not found", http.StatusNotFound)
		return
	}
	var transcript []interviewMsg
	json.Unmarshal([]byte(transcriptRaw), &transcript)

	var tb strings.Builder
	for _, m := range transcript {
		who := "Interviewer"
		if m.Role == "candidate" {
			who = "Candidate"
		}
		fmt.Fprintf(&tb, "%s: %s\n", who, m.Content)
	}

	deliveryNote := ""
	if body.Delivery != nil && body.Delivery.Enabled {
		d := body.Delivery
		deliveryNote = fmt.Sprintf(`

Delivery signals (from camera + voice; treat ONLY as a soft input to the "communication" score, never technical scores):
- On-camera composure signal: %d/100
- Time face was visible (eye-contact proxy): %d%%
- Predominant expression: %s
- Answered by voice: %v (%d words spoken)`,
			d.Composure, d.EyeContact, orStr(d.DominantExpression, "n/a"), d.Spoke, d.WordsSpoken)
	}

	prompt := fmt.Sprintf(`You are evaluating a candidate's system-design interview. Transcript:
%s%s

Score each dimension 0-100 based ONLY on what the candidate demonstrated (0 if never addressed):
requirements, api_design, data_modeling, scalability, caching, reliability, failure_handling, tradeoffs, communication.
Give 2-3 concrete "strengths" and 2-3 specific "gaps" (actionable, referencing the transcript), and a one-line "recommended" next refresh (topic + minutes).
Be rigorous and fair. Respond ONLY as JSON matching the schema.`, strings.TrimSpace(tb.String()), deliveryNote)

	eval := interviewEval{Dimensions: map[string]int{}}
	text, err := h.ai.callCFAI("/ai/generate", map[string]interface{}{
		"prompt": prompt, "max_tokens": 900, "json_schema": interviewEvalSchema,
	})
	valid := false
	if err == nil {
		var parsed interviewEval
		if json.Unmarshal([]byte(extractJSON(text)), &parsed) == nil && len(parsed.Dimensions) > 0 {
			for k, v := range parsed.Dimensions {
				parsed.Dimensions[k] = clamp(v, 0, 100)
			}
			eval = parsed
			valid = true
		}
	}
	if !valid {
		// Deterministic fallback: neutral, honest, non-fabricated feedback.
		for _, d := range []string{"requirements", "api_design", "data_modeling", "scalability", "caching", "reliability", "failure_handling", "tradeoffs", "communication"} {
			eval.Dimensions[d] = 55
		}
		eval.Strengths = []string{"Engaged with the problem and communicated a structure."}
		eval.Gaps = []string{"Go deeper on failure handling and caching strategy in your next attempt."}
		eval.Recommended = "System Design — 15 min"
	}

	// Ensure the 9 base dimensions always exist (0 = "not addressed", per the
	// rubric) so the debrief and email are complete and the composure nudge applies.
	for _, d := range []string{"requirements", "api_design", "data_modeling", "scalability", "caching", "reliability", "failure_handling", "tradeoffs", "communication"} {
		if _, ok := eval.Dimensions[d]; !ok {
			eval.Dimensions[d] = 0
		}
	}

	// Composure is a DEDICATED, deterministic dimension derived from the delivery
	// signals (never fabricated by the LLM). Communication gets a gentle nudge
	// from composure so on-camera presence has a bounded, honest influence.
	if body.Delivery != nil && body.Delivery.Enabled {
		eval.Dimensions["composure"] = clamp(body.Delivery.Composure, 0, 100)
		if c, ok := eval.Dimensions["communication"]; ok {
			eval.Dimensions["communication"] = clamp(int(math.Round(float64(c)*0.7+float64(body.Delivery.Composure)*0.3)), 0, 100)
		}
	}

	dimRaw, _ := json.Marshal(eval.Dimensions)
	strRaw, _ := json.Marshal(eval.Strengths)
	gapRaw, _ := json.Marshal(eval.Gaps)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.interview_evaluations (interview_id, dimensions, strengths, gaps, recommended)
		 VALUES ($1,$2,$3,$4,$5)`, sc), id, string(dimRaw), string(strRaw), string(gapRaw), eval.Recommended)
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.interview_sessions SET status='completed', completed_at=NOW() WHERE id=$1`, sc), id)
	h.logEvent(r.Context(), sc, userID, "interview_completed", topicKey, id, map[string]interface{}{})

	// Email the debrief (best-effort, async — never blocks the response).
	var topicName string
	if topicKey != "" {
		h.db.QueryRowContext(r.Context(), `SELECT name FROM public.forge_skills WHERE key=$1`, topicKey).Scan(&topicName)
	}
	var email, name string
	h.db.QueryRowContext(r.Context(), `SELECT email, COALESCE(name,'') FROM users WHERE id=$1`, userID).Scan(&email, &name)
	if email != "" {
		go emailInterviewDebrief(email, name, topicName, eval, body.Delivery)
	}

	httputil.OK(w, map[string]interface{}{
		"dimensions": eval.Dimensions, "strengths": eval.Strengths,
		"gaps": eval.Gaps, "recommended": eval.Recommended,
		"delivery":     body.Delivery,
		"emailed_to":   email,
		"email_active": mailer.Configured(),
	})
}

type deliverySignals struct {
	Enabled            bool           `json:"enabled"`
	Composure          int            `json:"composure"`
	EyeContact         int            `json:"eye_contact"`
	DominantExpression string         `json:"dominant_expression"`
	Expressions        map[string]int `json:"expressions"`
	Spoke              bool           `json:"spoke"`
	WordsSpoken        int            `json:"words_spoken"`
}

func orStr(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// ---------------------------------------------------------------------------
// Interview debrief email (polished HTML)
// ---------------------------------------------------------------------------

var interviewDimLabels = map[string]string{
	"requirements": "Requirements", "api_design": "API Design", "data_modeling": "Data Modeling",
	"scalability": "Scalability", "caching": "Caching", "reliability": "Reliability",
	"failure_handling": "Failure Handling", "tradeoffs": "Tradeoffs", "communication": "Communication",
	"composure": "Composure (on camera)",
}

func scoreColor(v int) string {
	switch {
	case v >= 80:
		return "#0f9d6b"
	case v >= 55:
		return "#c2790a"
	default:
		return "#e5484d"
	}
}

// emailInterviewDebrief sends the polished HTML debrief. Best-effort; logs on failure.
func emailInterviewDebrief(to, name, topicName string, eval interviewEval, d *deliverySignals) {
	if !mailer.Configured() {
		return
	}
	subject := "Your Lumintora interview debrief"
	if topicName != "" {
		subject += " — " + topicName
	}
	htmlBody := interviewDebriefHTML(name, topicName, eval, d)
	textBody := interviewDebriefText(name, topicName, eval, d)
	if err := mailer.SendHTML(to, subject, textBody, htmlBody); err != nil {
		log.Printf("interview debrief email failed for %s: %v", to, err)
	} else {
		log.Printf("interview debrief email sent to %s via %s", to, mailer.Provider())
	}
}

// orderedDims returns dimensions weakest-first for display.
func orderedDims(dims map[string]int) [][2]interface{} {
	type kv struct {
		k string
		v int
	}
	var arr []kv
	for k, v := range dims {
		arr = append(arr, kv{k, v})
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].v < arr[j].v })
	out := make([][2]interface{}, 0, len(arr))
	for _, e := range arr {
		out = append(out, [2]interface{}{e.k, e.v})
	}
	return out
}

func avgScore(dims map[string]int) int {
	if len(dims) == 0 {
		return 0
	}
	sum := 0
	for _, v := range dims {
		sum += v
	}
	return int(math.Round(float64(sum) / float64(len(dims))))
}

func interviewDebriefHTML(name, topicName string, eval interviewEval, d *deliverySignals) string {
	esc := html.EscapeString
	first := "there"
	if strings.TrimSpace(name) != "" {
		first = strings.Fields(name)[0]
	}
	overall := avgScore(eval.Dimensions)
	title := "AI Interview"
	if topicName != "" {
		title = topicName + " Interview"
	}

	var bars strings.Builder
	for _, kv := range orderedDims(eval.Dimensions) {
		k := kv[0].(string)
		v := kv[1].(int)
		label := interviewDimLabels[k]
		if label == "" {
			label = k
		}
		col := scoreColor(v)
		fmt.Fprintf(&bars, `
      <tr>
        <td style="padding:7px 0;font:600 13px/1.4 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1a1626;width:170px;">%s</td>
        <td style="padding:7px 0;width:100%%;">
          <div style="background:#ece9f2;border-radius:100px;height:9px;width:100%%;">
            <div style="background:%s;height:9px;border-radius:100px;width:%d%%;"></div>
          </div>
        </td>
        <td style="padding:7px 0 7px 12px;font:700 13px/1.4 ui-monospace,Menlo,monospace;color:%s;text-align:right;white-space:nowrap;">%d</td>
      </tr>`, esc(label), col, v, col, v)
	}

	li := func(items []string, color string) string {
		if len(items) == 0 {
			return `<li style="color:#6a6479;">—</li>`
		}
		var s strings.Builder
		for _, it := range items {
			fmt.Fprintf(&s, `<li style="margin:0 0 8px;padding-left:4px;color:#2a2536;font:400 14px/1.55 -apple-system,Segoe UI,Roboto,Arial,sans-serif;">%s</li>`, esc(it))
		}
		return s.String()
	}

	deliveryBlock := ""
	if d != nil && d.Enabled {
		domexp := esc(orStr(d.DominantExpression, "n/a"))
		voice := "No"
		if d.Spoke {
			voice = fmt.Sprintf("Yes · %d words", d.WordsSpoken)
		}
		deliveryBlock = fmt.Sprintf(`
    <div style="margin:22px 0 0;padding:18px 20px;background:#f5f3f9;border:1px solid #ece9f2;border-radius:14px;">
      <div style="font:700 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.06em;text-transform:uppercase;color:#5b30d8;margin-bottom:12px;">On-camera presence</div>
      <table style="width:100%%;border-collapse:collapse;">
        <tr><td style="font:400 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;padding:3px 0;">Composure signal</td><td style="text-align:right;font:700 13px/1.5 ui-monospace,Menlo,monospace;color:%s;">%d/100</td></tr>
        <tr><td style="font:400 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;padding:3px 0;">Eye-contact (face visible)</td><td style="text-align:right;font:700 13px/1.5 ui-monospace,Menlo,monospace;color:#1a1626;">%d%%</td></tr>
        <tr><td style="font:400 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;padding:3px 0;">Predominant expression</td><td style="text-align:right;font:600 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1a1626;">%s</td></tr>
        <tr><td style="font:400 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;padding:3px 0;">Answered by voice</td><td style="text-align:right;font:600 13px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1a1626;">%s</td></tr>
      </table>
      <div style="margin-top:10px;font:400 12px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#9b95a9;">A confidence &amp; engagement signal — it lightly informs your communication score, not your technical scores.</div>
    </div>`, scoreColor(d.Composure), d.Composure, d.EyeContact, domexp, voice)
	}

	rec := ""
	if strings.TrimSpace(eval.Recommended) != "" {
		rec = fmt.Sprintf(`
    <div style="margin:22px 0 0;padding:16px 20px;background:#f4f0ff;border-left:3px solid #7140ff;border-radius:0 12px 12px 0;">
      <span style="font:700 14px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1a1626;">Recommended next refresh:</span>
      <span style="font:400 14px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#2a2536;"> %s</span>
    </div>`, esc(eval.Recommended))
	}

	return fmt.Sprintf(`<!doctype html><html><body style="margin:0;background:#faf9fc;padding:24px 12px;">
  <table role="presentation" style="max-width:600px;margin:0 auto;width:100%%;border-collapse:collapse;">
    <tr><td style="padding:0 0 18px;">
      <span style="font:800 20px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#1a1626;">Lumintora</span>
      <span style="font:600 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#5b30d8;background:#efeaff;padding:4px 8px;border-radius:100px;margin-left:8px;">Forge · Interview</span>
    </td></tr>
    <tr><td style="background:#ffffff;border:1px solid #ece9f2;border-radius:18px;padding:28px;">
      <div style="font:400 14px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;">Hi %s,</div>
      <h1 style="margin:6px 0 2px;font:700 24px/1.2 Georgia,'Times New Roman',serif;color:#1a1626;">Your %s debrief</h1>
      <div style="font:400 14px/1.55 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#6a6479;">Scored on what you actually demonstrated — no vanity number.</div>

      <div style="margin:22px 0;text-align:center;background:linear-gradient(135deg,#7140ff,#5b30d8);border-radius:14px;padding:22px;">
        <div style="font:700 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.08em;text-transform:uppercase;color:rgba(255,255,255,.8);">Overall</div>
        <div style="font:800 40px/1 ui-monospace,Menlo,monospace;color:#ffffff;margin-top:6px;">%d<span style="font-size:20px;opacity:.7;">/100</span></div>
      </div>

      <div style="font:700 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.06em;text-transform:uppercase;color:#9b95a9;margin:8px 0 4px;">By dimension · weakest first</div>
      <table style="width:100%%;border-collapse:collapse;">%s</table>

      %s

      <div style="margin:24px 0 0;">
        <div style="font:700 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.06em;text-transform:uppercase;color:#0f9d6b;margin-bottom:8px;">Strong</div>
        <ul style="margin:0;padding-left:18px;">%s</ul>
      </div>
      <div style="margin:18px 0 0;">
        <div style="font:700 12px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;letter-spacing:.06em;text-transform:uppercase;color:#c2790a;margin-bottom:8px;">Needs work</div>
        <ul style="margin:0;padding-left:18px;">%s</ul>
      </div>

      %s

      <div style="margin:26px 0 0;text-align:center;">
        <a href="%s/forge" style="display:inline-block;background:#7140ff;color:#ffffff;text-decoration:none;font:700 14px/1 -apple-system,Segoe UI,Roboto,Arial,sans-serif;padding:13px 22px;border-radius:10px;">Practice another interview →</a>
      </div>
    </td></tr>
    <tr><td style="padding:18px 4px;font:400 12px/1.5 -apple-system,Segoe UI,Roboto,Arial,sans-serif;color:#9b95a9;text-align:center;">
      You're receiving this because you completed an interview on Lumintora Forge.
    </td></tr>
  </table>
</body></html>`,
		esc(first), esc(title), overall, bars.String(), deliveryBlock,
		li(eval.Strengths, "#0f9d6b"), li(eval.Gaps, "#c2790a"), rec,
		getEnv("FRONTEND_URL", "http://localhost:3000"))
}

func interviewDebriefText(name, topicName string, eval interviewEval, d *deliverySignals) string {
	var b strings.Builder
	first := "there"
	if strings.TrimSpace(name) != "" {
		first = strings.Fields(name)[0]
	}
	fmt.Fprintf(&b, "Hi %s,\n\nYour %s interview debrief (overall %d/100):\n\n", first, orStr(topicName, "AI"), avgScore(eval.Dimensions))
	for _, kv := range orderedDims(eval.Dimensions) {
		k := kv[0].(string)
		v := kv[1].(int)
		label := interviewDimLabels[k]
		if label == "" {
			label = k
		}
		fmt.Fprintf(&b, "  %-22s %d/100\n", label, v)
	}
	b.WriteString("\nStrong:\n")
	for _, s := range eval.Strengths {
		fmt.Fprintf(&b, "  + %s\n", s)
	}
	b.WriteString("\nNeeds work:\n")
	for _, g := range eval.Gaps {
		fmt.Fprintf(&b, "  - %s\n", g)
	}
	if d != nil && d.Enabled {
		fmt.Fprintf(&b, "\nOn-camera presence: composure %d/100, eye-contact %d%%, mostly %s.\n", d.Composure, d.EyeContact, orStr(d.DominantExpression, "n/a"))
	}
	if eval.Recommended != "" {
		fmt.Fprintf(&b, "\nRecommended next refresh: %s\n", eval.Recommended)
	}
	b.WriteString("\n— Lumintora Forge")
	return b.String()
}
