package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
)

type FeedbackHandler struct{ db *sql.DB }

func NewFeedbackHandler(db *sql.DB) *FeedbackHandler { return &FeedbackHandler{db: db} }

func (h *FeedbackHandler) Submit(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)

	var req struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		College     string `json:"college"`
		Branch      string `json:"branch"`
		Year        string `json:"year"`
		Category    string `json:"category"`
		Subject     string `json:"subject"`
		Description string `json:"description"`
		Severity    string `json:"severity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Subject = strings.TrimSpace(req.Subject)
	req.Description = strings.TrimSpace(req.Description)
	req.Category = strings.TrimSpace(req.Category)

	if req.Name == "" || req.Email == "" || req.Category == "" || req.Subject == "" || req.Description == "" {
		httputil.Error(w, "name, email, category, subject and description are required", http.StatusBadRequest)
		return
	}

	var id string
	err := h.db.QueryRowContext(r.Context(),
		`INSERT INTO feedback (user_id, name, email, college, branch, year, category, subject, description, severity)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		userID,
		req.Name, req.Email,
		nilStr(req.College), nilStr(req.Branch), nilStr(req.Year),
		req.Category, req.Subject, req.Description,
		nilStr(req.Severity),
	).Scan(&id)
	if err != nil {
		httputil.Error(w, "could not submit feedback", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, map[string]string{"id": id, "status": "received"})
}

func nilStr(s string) interface{} {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
