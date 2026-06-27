package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"

	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
)

const defaultCFURL = "https://lumintora-ai.bobbynandigam-official.workers.dev"

type AIHandler struct {
	db     *sql.DB
	cfURL  string
	client *http.Client
}

func NewAIHandler(db *sql.DB) *AIHandler {
	return &AIHandler{
		db:     db,
		cfURL:  getEnv("CF_AI_URL", defaultCFURL),
		client: &http.Client{Timeout: 150 * time.Second},
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (h *AIHandler) callCFAI(endpoint string, payload interface{}) (string, error) {
	body, _ := json.Marshal(payload)
	resp, err := h.client.Post(h.cfURL+endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		if resp.StatusCode >= 400 {
			return "", fmt.Errorf("AI worker returned %d: %s", resp.StatusCode, string(data))
		}
		return string(data), nil
	}
	if msg, ok := result["error"].(string); ok && msg != "" {
		return "", fmt.Errorf("AI worker error: %s", msg)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("AI worker returned status %d", resp.StatusCode)
	}
	if text, ok := result["text"].(string); ok {
		return text, nil
	}
	return string(data), nil
}

func (h *AIHandler) GeneratePath(w http.ResponseWriter, r *http.Request) {
	var req models.GeneratePathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	userID := middleware.GetUserID(r)

	prompt := fmt.Sprintf(`You are an adaptive learning system. Generate a structured learning path for:
Goal: %s
Level: %s
Topic: %s

Respond ONLY with valid JSON in this exact format:
{
  "title": "path title",
  "description": "brief description",
  "topic": "main topic",
  "estimated_hours": 20,
  "tags": ["tag1","tag2"],
  "modules": [
    {
      "title": "Module title",
      "description": "What you will learn",
      "content": "Detailed lesson content here with examples...",
      "type": "lesson",
      "order_index": 1,
      "duration_minutes": 20,
      "xp_reward": 10,
      "difficulty": "beginner"
    }
  ]
}
Include 6-8 modules. Types can be: lesson, quiz, project, code. Mix them appropriately.`, req.Goal, req.Level, req.Topic)

	aiPayload := map[string]interface{}{
		"prompt":      prompt,
		"max_tokens":  4096,
		"json_schema": pathJSONSchema,
	}
	if req.Model != "" {
		aiPayload["model"] = req.Model
	}
	text, err := h.callCFAI("/ai/generate", aiPayload)
	if err != nil {
		httputil.Error(w, "AI service unavailable, please try again", http.StatusServiceUnavailable)
		return
	}

	var aiPath struct {
		Title          string   `json:"title"`
		Description    string   `json:"description"`
		Topic          string   `json:"topic"`
		EstimatedHours int      `json:"estimated_hours"`
		Tags           []string `json:"tags"`
		Modules        []struct {
			Title           string `json:"title"`
			Description     string `json:"description"`
			Content         string `json:"content"`
			Type            string `json:"type"`
			OrderIndex      int    `json:"order_index"`
			DurationMinutes int    `json:"duration_minutes"`
			XPReward        int    `json:"xp_reward"`
			Difficulty      string `json:"difficulty"`
		} `json:"modules"`
	}

	cleanText := extractJSON(text)
	if err := json.Unmarshal([]byte(cleanText), &aiPath); err != nil || len(aiPath.Modules) == 0 {
		httputil.Error(w, "AI returned an invalid path, please try again", http.StatusBadGateway)
		return
	}

	var path models.LearningPath
	err = h.db.QueryRowContext(r.Context(),
		`INSERT INTO learning_paths (user_id, title, description, goal, topic, level, estimated_hours, tags, total_modules)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, user_id, title, description, goal, topic, level, status, progress,
		           total_modules, completed_modules, estimated_hours, created_at, updated_at`,
		userID, aiPath.Title, aiPath.Description, req.Goal, aiPath.Topic, req.Level,
		aiPath.EstimatedHours, pq.Array(aiPath.Tags), len(aiPath.Modules),
	).Scan(
		&path.ID, &path.UserID, &path.Title, &path.Description, &path.Goal, &path.Topic,
		&path.Level, &path.Status, &path.Progress, &path.TotalModules, &path.CompletedModules,
		&path.EstimatedHours, &path.CreatedAt, &path.UpdatedAt,
	)
	if err != nil {
		httputil.Error(w, "could not save path: "+err.Error(), http.StatusInternalServerError)
		return
	}
	path.Tags = aiPath.Tags

	for _, mod := range aiPath.Modules {
		var m models.Module
		h.db.QueryRowContext(r.Context(),
			`INSERT INTO modules (path_id, title, description, content, type, order_index, duration_minutes, xp_reward, difficulty)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 RETURNING id, path_id, title, description, type, order_index, duration_minutes, xp_reward, difficulty, created_at`,
			path.ID, mod.Title, mod.Description, "", mod.Type, mod.OrderIndex,
			mod.DurationMinutes, mod.XPReward, mod.Difficulty,
		).Scan(&m.ID, &m.PathID, &m.Title, &m.Description, &m.Type, &m.OrderIndex,
			&m.DurationMinutes, &m.XPReward, &m.Difficulty, &m.CreatedAt)
		m.Status = "locked"
		if mod.OrderIndex == 1 {
			m.Status = "available"
		}
		path.Modules = append(path.Modules, m)
	}

	if len(path.Modules) > 0 {
		h.db.ExecContext(r.Context(),
			`INSERT INTO user_module_progress (user_id, module_id, path_id, status)
			 VALUES ($1,$2,$3,'available') ON CONFLICT DO NOTHING`,
			userID, path.Modules[0].ID, path.ID,
		)
	}

	httputil.Created(w, path)
}

func (h *AIHandler) Adapt(w http.ResponseWriter, r *http.Request) {
	pathID := chi.URLParam(r, "pathID")
	userID := middleware.GetUserID(r)

	var req struct {
		TriggerModuleID string `json:"trigger_module_id"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	var pTitle, topic, level, goal string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT title, COALESCE(topic,''), COALESCE(level,'beginner'), COALESCE(goal,'')
		 FROM learning_paths WHERE id=$1 AND user_id=$2`, pathID, userID,
	).Scan(&pTitle, &topic, &level, &goal); err != nil {
		httputil.Error(w, "path not found", http.StatusNotFound)
		return
	}

	var triggerOrder int
	var triggerID, triggerTitle string
	if req.TriggerModuleID != "" {
		h.db.QueryRowContext(r.Context(),
			`SELECT id, order_index, title FROM modules WHERE id=$1 AND path_id=$2`,
			req.TriggerModuleID, pathID,
		).Scan(&triggerID, &triggerOrder, &triggerTitle)
	}
	if triggerID == "" {
		h.db.QueryRowContext(r.Context(),
			`SELECT m.id, m.order_index, m.title FROM modules m
			 JOIN user_module_progress ump ON ump.module_id=m.id
			 WHERE ump.user_id=$1 AND m.path_id=$2 AND ump.status='completed'
			 ORDER BY m.order_index DESC LIMIT 1`, userID, pathID,
		).Scan(&triggerID, &triggerOrder, &triggerTitle)
	}
	if triggerID == "" {
		httputil.OK(w, map[string]interface{}{"adapted": false, "reason": "complete a module first"})
		return
	}

	var dup int
	h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM path_adaptations WHERE path_id=$1 AND user_id=$2 AND trigger_module_id=$3`,
		pathID, userID, triggerID,
	).Scan(&dup)
	if dup > 0 {
		httputil.OK(w, map[string]interface{}{"adapted": false, "reason": "already adapted here"})
		return
	}

	var triggerFb sql.NullString
	var triggerScore sql.NullInt64
	h.db.QueryRowContext(r.Context(),
		`SELECT difficulty_feedback, NULLIF(score,0) FROM user_module_progress
		 WHERE user_id=$1 AND module_id=$2`, userID, triggerID,
	).Scan(&triggerFb, &triggerScore)

	var avgScore sql.NullFloat64
	h.db.QueryRowContext(r.Context(),
		`SELECT AVG(score) FROM user_module_progress WHERE user_id=$1 AND path_id=$2 AND score>0`,
		userID, pathID,
	).Scan(&avgScore)

	direction := ""
	switch {
	case triggerFb.String == "hard" ||
		(triggerScore.Valid && triggerScore.Int64 < 60) ||
		(avgScore.Valid && avgScore.Float64 < 55):
		direction = "remediate"
	case triggerFb.String == "easy" ||
		(triggerScore.Valid && triggerScore.Int64 >= 90 && avgScore.Valid && avgScore.Float64 >= 85):
		direction = "advance"
	}
	if direction == "" {
		httputil.OK(w, map[string]interface{}{"adapted": false, "reason": "you're right on track"})
		return
	}

	var guide, wantType string
	if direction == "remediate" {
		guide = fmt.Sprintf(`The learner is finding the material on "%s" too difficult. Design ONE short, encouraging
remedial module that fills the most likely missing foundation so they can confidently retry. Make it a step
EASIER than the rest of the course, very concrete, and quick.`, triggerTitle)
		wantType = "lesson"
	} else {
		guide = fmt.Sprintf(`The learner is finding "%s" too easy and is ready for more. Design ONE more challenging module
that stretches them — a deeper dive or a hands-on mini-project that applies the idea in a harder context.`, triggerTitle)
		wantType = "project"
	}

	prompt := fmt.Sprintf(`%s

Course: %s
Topic: %s
Overall learner level: %s
Learner goal: %s

Respond ONLY with JSON for a single module. The "adaptive_reason" must be a short, friendly one-sentence
explanation addressed to the learner (e.g. "Added to shore up %s before you move on").
Pick a "type" of one of: lesson, quiz, project, code (prefer %q). Keep duration_minutes 10-25 and xp_reward 10-30.`,
		guide, pTitle, topic, level, goal, triggerTitle, wantType)

	text, err := h.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":      prompt,
		"max_tokens":  700,
		"json_schema": moduleJSONSchema,
	})
	if err != nil {
		httputil.Error(w, "AI service unavailable, please try again", http.StatusServiceUnavailable)
		return
	}

	var m struct {
		Title           string `json:"title"`
		Description     string `json:"description"`
		Type            string `json:"type"`
		DurationMinutes int    `json:"duration_minutes"`
		XPReward        int    `json:"xp_reward"`
		Difficulty      string `json:"difficulty"`
		AdaptiveReason  string `json:"adaptive_reason"`
	}
	if err := json.Unmarshal([]byte(extractJSON(text)), &m); err != nil || m.Title == "" {
		httputil.Error(w, "could not build an adaptive module, please try again", http.StatusServiceUnavailable)
		return
	}
	if m.Type == "" {
		m.Type = wantType
	}
	if m.DurationMinutes == 0 {
		m.DurationMinutes = 15
	}
	if m.XPReward == 0 {
		m.XPReward = 15
	}
	if m.Difficulty == "" {
		if direction == "remediate" {
			m.Difficulty = "beginner"
		} else {
			m.Difficulty = "advanced"
		}
	}

	insertOrder := triggerOrder + 1
	h.db.ExecContext(r.Context(),
		`UPDATE modules SET order_index=order_index+1 WHERE path_id=$1 AND order_index>=$2`,
		pathID, insertOrder,
	)
	var newID string
	if err := h.db.QueryRowContext(r.Context(),
		`INSERT INTO modules (path_id, title, description, content, type, order_index,
		                      duration_minutes, xp_reward, difficulty, source, adaptive_reason)
		 VALUES ($1,$2,$3,'',$4,$5,$6,$7,$8,'adaptive',$9) RETURNING id`,
		pathID, m.Title, m.Description, m.Type, insertOrder,
		m.DurationMinutes, m.XPReward, m.Difficulty, m.AdaptiveReason,
	).Scan(&newID); err != nil {
		httputil.Error(w, "could not save adaptive module", http.StatusInternalServerError)
		return
	}

	h.db.ExecContext(r.Context(),
		`INSERT INTO user_module_progress (user_id, module_id, path_id, status)
		 VALUES ($1,$2,$3,'available') ON CONFLICT DO NOTHING`,
		userID, newID, pathID,
	)
	h.db.ExecContext(r.Context(),
		`UPDATE learning_paths SET total_modules=total_modules+1,
		   progress=LEAST(100, (SELECT COUNT(*)*100/GREATEST(total_modules,1)
		     FROM user_module_progress WHERE path_id=$1 AND user_id=$2 AND status='completed')),
		   updated_at=NOW() WHERE id=$1`,
		pathID, userID,
	)
	h.db.ExecContext(r.Context(),
		`INSERT INTO path_adaptations (path_id, user_id, trigger_module_id, direction, reason, created_module_id)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		pathID, userID, triggerID, direction, m.AdaptiveReason, newID,
	)

	httputil.OK(w, map[string]interface{}{
		"adapted":   true,
		"direction": direction,
		"reason":    m.AdaptiveReason,
		"module": map[string]interface{}{
			"id": newID, "title": m.Title, "description": m.Description,
			"type": m.Type, "difficulty": m.Difficulty, "order_index": insertOrder,
		},
	})
}

func (h *AIHandler) Explain(w http.ResponseWriter, r *http.Request) {
	var req models.ExplainRequest
	json.NewDecoder(r.Body).Decode(&req)

	prompt := fmt.Sprintf(`Explain this concept clearly and concisely for a learner:
Concept: %s
Context: %s

Give a clear explanation with an analogy and a practical example. Keep it under 200 words.`, req.Concept, req.Context)

	text, err := h.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil {
		httputil.Error(w, "AI unavailable", http.StatusServiceUnavailable)
		return
	}
	httputil.OK(w, map[string]string{"explanation": text})
}

func (h *AIHandler) Hint(w http.ResponseWriter, r *http.Request) {
	var req models.HintRequest
	json.NewDecoder(r.Body).Decode(&req)

	prompt := fmt.Sprintf(`A student is stuck on this problem:
Problem: %s
Their attempt: %s

Give a helpful hint that guides them without giving away the answer. Be Socratic.`, req.Problem, req.Attempt)

	text, err := h.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil {
		httputil.Error(w, "AI unavailable", http.StatusServiceUnavailable)
		return
	}
	httputil.OK(w, map[string]string{"hint": text})
}

func (h *AIHandler) EvaluateCode(w http.ResponseWriter, r *http.Request) {
	var req models.EvalCodeRequest
	json.NewDecoder(r.Body).Decode(&req)

	prompt := fmt.Sprintf(`Evaluate this %s code for the problem: %s

Code:
%s

Provide: 1) Correctness assessment, 2) Code quality feedback, 3) One improvement suggestion.
Be concise and encouraging.`, req.Language, req.Problem, req.Code)

	text, err := h.callCFAI("/ai/generate", map[string]string{"prompt": prompt})
	if err != nil {
		httputil.Error(w, "AI unavailable", http.StatusServiceUnavailable)
		return
	}
	httputil.OK(w, map[string]string{"feedback": text})
}

func (h *AIHandler) GetContent(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")

	var title, description, mtype, content, topic, level string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT m.title, COALESCE(m.description,''), m.type, COALESCE(m.content,''),
		        COALESCE(p.topic,''), COALESCE(p.level,'beginner')
		 FROM modules m JOIN learning_paths p ON p.id = m.path_id
		 WHERE m.id=$1`, moduleID,
	).Scan(&title, &description, &mtype, &content, &topic, &level)
	if err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}

	if len(strings.TrimSpace(content)) >= 400 {
		httputil.OK(w, map[string]string{"content": content, "generated": "false"})
		return
	}

	generated, err := h.generateAndStoreContent(r.Context(), moduleID, title, description, mtype, topic, level)
	if err != nil {
		httputil.Error(w, "AI service unavailable, please try again", http.StatusServiceUnavailable)
		return
	}
	httputil.OK(w, map[string]string{"content": generated, "generated": "true"})
}

func (h *AIHandler) generateAndStoreContent(ctx context.Context, moduleID, title, description, mtype, topic, level string) (string, error) {
	guide := "Write a clear, engaging lesson that teaches the concept from first principles, using analogies, concrete examples, and short code snippets where useful."
	if mtype == "code" || mtype == "project" {
		guide = "Write a hands-on lesson: briefly explain the concepts the learner needs, then walk through a clear, step-by-step build or challenge with example code in fenced code blocks."
	}

	prompt := fmt.Sprintf(`%s

Course topic: %s
Learner level: %s
Module title: %s
What this module should teach: %s

Produce a visually rich, structured lesson in GitHub-flavored Markdown. You MUST use these elements:

1. Open with a one-line **TL;DR:** summary, then a "## Why it matters" hook.
2. Include AT LEAST ONE Mermaid diagram inside a fenced code block tagged "mermaid" that genuinely
   illuminates the topic — a flowchart ("flowchart TD"), a sequence diagram ("sequenceDiagram"), or a
   mindmap ("mindmap"). Keep node labels short and the syntax valid. Example:
   `+"```"+`mermaid
   flowchart TD
     A[Request] --> B{Authenticated?}
     B -- Yes --> C[Serve data]
     B -- No --> D[401]
   `+"```"+`
3. Use callout cards for emphasis — exactly these forms, each on its own line:
   > [!KEY] the single most important idea
   > [!TIP] a practical pro-tip
   > [!WARNING] a common mistake to avoid
   > [!NOTE] a useful aside
4. Teach with a concrete **worked example** and a relatable **analogy**.
5. Where code helps, use fenced code blocks with the correct language tag.
6. Use a Markdown table when comparing options/approaches.
7. End with a "## Recap" of 3-4 bullet takeaways.

Use ## section headings, short paragraphs, **bold** for key terms. Aim for 500-850 words.
Begin directly with the TL;DR line — no preamble and do not repeat the module title as a heading.`,
		guide, topic, level, title, description)

	text, err := h.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":     prompt,
		"max_tokens": 3200,
	})
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(text)
	if content == "" {
		return "", fmt.Errorf("AI returned empty content")
	}

	h.db.ExecContext(ctx, `UPDATE modules SET content=$1, updated_at=NOW() WHERE id=$2`, content, moduleID)
	return content, nil
}

func (h *AIHandler) GetQuiz(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")

	var title, description, content, topic string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT m.title, COALESCE(m.description,''), COALESCE(m.content,''), COALESCE(p.topic,'')
		 FROM modules m JOIN learning_paths p ON p.id = m.path_id
		 WHERE m.id=$1`, moduleID,
	).Scan(&title, &description, &content, &topic)
	if err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}

	questions, err := h.loadQuiz(r.Context(), moduleID)
	if err != nil {
		httputil.Error(w, "could not load quiz", http.StatusInternalServerError)
		return
	}
	if len(questions) == 0 {
		questions, err = h.generateAndStoreQuiz(r.Context(), moduleID, title, description, content, topic)
		if err != nil {
			httputil.Error(w, "AI service unavailable, please try again", http.StatusServiceUnavailable)
			return
		}
	}

	out := make([]models.QuizQuestion, 0, len(questions))
	for _, q := range questions {
		out = append(out, models.QuizQuestion{
			ID:         q.ID,
			ModuleID:   q.ModuleID,
			Question:   q.Question,
			Options:    q.Options,
			OrderIndex: q.OrderIndex,
		})
	}
	httputil.OK(w, out)
}

func (h *AIHandler) loadQuiz(ctx context.Context, moduleID string) ([]models.QuizQuestion, error) {
	rows, err := h.db.QueryContext(ctx,
		`SELECT id, module_id, question, options, correct_option, COALESCE(explanation,''), order_index
		 FROM quiz_questions WHERE module_id=$1 ORDER BY order_index`, moduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var qs []models.QuizQuestion
	for rows.Next() {
		var q models.QuizQuestion
		var optsJSON []byte
		if err := rows.Scan(&q.ID, &q.ModuleID, &q.Question, &optsJSON, &q.CorrectOption, &q.Explanation, &q.OrderIndex); err != nil {
			continue
		}
		json.Unmarshal(optsJSON, &q.Options)
		qs = append(qs, q)
	}
	return qs, nil
}

func (h *AIHandler) generateAndStoreQuiz(ctx context.Context, moduleID, title, description, content, topic string) ([]models.QuizQuestion, error) {
	prompt := fmt.Sprintf(`Create a multiple-choice quiz that tests understanding of this lesson.

Topic: %s
Module: %s
Summary: %s
Lesson content:
%s

Write exactly 4 questions. Each question must have exactly 4 options, exactly one
correct answer (correct_option is its 0-based index into options), and a one-sentence
explanation of why it is correct. Respond ONLY with valid JSON.`,
		topic, title, description, truncate(content, 2000))

	text, err := h.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":      prompt,
		"max_tokens":  2048,
		"json_schema": quizJSONSchema,
	})
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Questions []struct {
			Question      string   `json:"question"`
			Options       []string `json:"options"`
			CorrectOption int      `json:"correct_option"`
			Explanation   string   `json:"explanation"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(extractJSON(text)), &parsed); err != nil || len(parsed.Questions) == 0 {
		return nil, fmt.Errorf("AI returned an invalid quiz")
	}

	var out []models.QuizQuestion
	for i, q := range parsed.Questions {
		if len(q.Options) < 2 {
			continue
		}
		if q.CorrectOption < 0 || q.CorrectOption >= len(q.Options) {
			q.CorrectOption = 0
		}
		optsJSON, _ := json.Marshal(q.Options)
		var id string
		err := h.db.QueryRowContext(ctx,
			`INSERT INTO quiz_questions (module_id, question, options, correct_option, explanation, order_index)
			 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			moduleID, q.Question, string(optsJSON), q.CorrectOption, q.Explanation, i+1,
		).Scan(&id)
		if err != nil {
			continue
		}
		out = append(out, models.QuizQuestion{
			ID:            id,
			ModuleID:      moduleID,
			Question:      q.Question,
			Options:       q.Options,
			CorrectOption: q.CorrectOption,
			Explanation:   q.Explanation,
			OrderIndex:    i + 1,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid quiz questions produced")
	}
	return out, nil
}

func (h *AIHandler) GenerateResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Objective  string `json:"objective"`
		TargetRole string `json:"target_role"`
		Experiences []struct {
			Company  string   `json:"company"`
			Role     string   `json:"role"`
			Location string   `json:"location"`
			Start    string   `json:"start"`
			End      string   `json:"end"`
			Current  bool     `json:"current"`
			Bullets  []string `json:"bullets"`
		} `json:"experiences"`
		Education []struct {
			School string `json:"school"`
			Degree string `json:"degree"`
			Field  string `json:"field"`
			Start  string `json:"start"`
			End    string `json:"end"`
			GPA    string `json:"gpa"`
		} `json:"education"`
		Skills   []string `json:"skills"`
		Projects []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Tech        string `json:"tech"`
			Link        string `json:"link"`
		} `json:"projects"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httputil.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	var expStr strings.Builder
	for i, e := range req.Experiences {
		end := e.End
		if e.Current {
			end = "Present"
		}
		expStr.WriteString(fmt.Sprintf("%d. %s at %s (%s – %s)\n", i+1, e.Role, e.Company, e.Start, end))
		for _, b := range e.Bullets {
			if strings.TrimSpace(b) != "" {
				expStr.WriteString(fmt.Sprintf("   - %s\n", b))
			}
		}
	}

	role := req.TargetRole
	if role == "" {
		role = "the target position"
	}

	prompt := fmt.Sprintf(`You are a professional resume writer helping %s apply for a role as %s.

Career notes / objective: %s

Work experience:
%s

Skills: %s

Tasks:
1. Write a compelling 2–3 sentence professional summary in third person (no "I" or "my"). Focus on years of experience, key strengths, and what the candidate brings.
2. For each work experience listed above, produce 3–5 polished bullet points that start with a strong past-tense action verb (Led, Built, Designed, Reduced, Increased, Launched, etc.), are specific, and quantify impact where plausible. Return one array of bullets per experience in the same order they were listed.

Return ONLY valid JSON — no prose, no markdown.`,
		req.Name, role, req.Objective, expStr.String(), strings.Join(req.Skills, ", "))

	text, err := h.callCFAI("/ai/generate", map[string]interface{}{
		"prompt":      prompt,
		"max_tokens":  2200,
		"json_schema": resumeJSONSchema,
	})
	if err != nil {
		httputil.Error(w, "AI service unavailable, please try again", http.StatusServiceUnavailable)
		return
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(extractJSON(text)), &result); err != nil {
		httputil.Error(w, "AI returned an invalid response, please try again", http.StatusBadGateway)
		return
	}

	httputil.OK(w, result)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end < start {
		return s
	}
	return s[start : end+1]
}

var pathJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"title":           map[string]interface{}{"type": "string"},
		"description":     map[string]interface{}{"type": "string"},
		"topic":           map[string]interface{}{"type": "string"},
		"estimated_hours": map[string]interface{}{"type": "integer"},
		"tags": map[string]interface{}{
			"type":  "array",
			"items": map[string]interface{}{"type": "string"},
		},
		"modules": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":            map[string]interface{}{"type": "string"},
					"description":      map[string]interface{}{"type": "string"},
					"content":          map[string]interface{}{"type": "string"},
					"type":             map[string]interface{}{"type": "string", "enum": []string{"lesson", "quiz", "project", "code"}},
					"order_index":      map[string]interface{}{"type": "integer"},
					"duration_minutes": map[string]interface{}{"type": "integer"},
					"xp_reward":        map[string]interface{}{"type": "integer"},
					"difficulty":       map[string]interface{}{"type": "string"},
				},
				"required": []string{"title", "description", "content", "type", "order_index"},
			},
		},
	},
	"required": []string{"title", "description", "topic", "estimated_hours", "modules"},
}

var moduleJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"title":            map[string]interface{}{"type": "string"},
		"description":      map[string]interface{}{"type": "string"},
		"type":             map[string]interface{}{"type": "string", "enum": []string{"lesson", "quiz", "project", "code"}},
		"duration_minutes": map[string]interface{}{"type": "integer"},
		"xp_reward":        map[string]interface{}{"type": "integer"},
		"difficulty":       map[string]interface{}{"type": "string"},
		"adaptive_reason":  map[string]interface{}{"type": "string"},
	},
	"required": []string{"title", "description", "type", "adaptive_reason"},
}

var quizJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"questions": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"question":       map[string]interface{}{"type": "string"},
					"options":        map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
					"correct_option": map[string]interface{}{"type": "integer"},
					"explanation":    map[string]interface{}{"type": "string"},
				},
				"required": []string{"question", "options", "correct_option"},
			},
		},
	},
	"required": []string{"questions"},
}

var resumeJSONSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"summary": map[string]interface{}{"type": "string"},
		"experiences": map[string]interface{}{
			"type": "array",
			"items": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"bullets": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"required": []string{"bullets"},
			},
		},
	},
	"required": []string{"summary", "experiences"},
}
