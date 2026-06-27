package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"

	"github.com/go-chi/chi/v5"
)

type QuizHandler struct {
	db *sql.DB
}

func NewQuizHandler(db *sql.DB) *QuizHandler {
	return &QuizHandler{db: db}
}

// SubmitQuiz grades answers against stored correct options and records the attempt.
func (h *QuizHandler) SubmitQuiz(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")
	userID := middleware.GetUserID(r)

	var req models.QuizSubmitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT correct_option, COALESCE(explanation,'') FROM quiz_questions WHERE module_id=$1 ORDER BY order_index`,
		moduleID,
	)
	if err != nil {
		httputil.Error(w, "questions not found", http.StatusNotFound)
		return
	}
	defer rows.Close()

	var correctAnswers []int
	explanations := []string{}
	for rows.Next() {
		var c int
		var e string
		rows.Scan(&c, &e)
		correctAnswers = append(correctAnswers, c)
		explanations = append(explanations, e)
	}
	if len(correctAnswers) == 0 {
		httputil.Error(w, "no quiz questions for this module", http.StatusNotFound)
		return
	}

	score := 0
	for i, ans := range req.Answers {
		if i < len(correctAnswers) && ans == correctAnswers[i] {
			score++
		}
	}

	total := len(correctAnswers)
	percent := score * 100 / total
	passed := percent >= 70

	h.db.ExecContext(r.Context(),
		`INSERT INTO user_module_progress (user_id, module_id, path_id, status, score, attempts)
		 SELECT $1, $2, m.path_id, 'in_progress', $3, 1 FROM modules m WHERE m.id=$2
		 ON CONFLICT (user_id, module_id) DO UPDATE SET
		   score=GREATEST(user_module_progress.score, EXCLUDED.score),
		   attempts=user_module_progress.attempts+1`,
		userID, moduleID, percent,
	)

	httputil.OK(w, map[string]interface{}{
		"score":           score,
		"total":           total,
		"percent":         percent,
		"passed":          passed,
		"correct_options": correctAnswers,
		"explanations":    explanations,
	})
}
