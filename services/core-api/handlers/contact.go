package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
)

type ContactHandler struct{ db *sql.DB }

func NewContactHandler(db *sql.DB) *ContactHandler { return &ContactHandler{db: db} }

func (h *ContactHandler) Submit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Email   string `json:"email"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.Message = strings.TrimSpace(req.Message)

	if req.Name == "" || req.Email == "" || req.Message == "" {
		httputil.Error(w, "name, email and message are required", http.StatusBadRequest)
		return
	}

	var id string
	err := h.db.QueryRowContext(r.Context(),
		`INSERT INTO feedback (user_id, name, email, category, subject, description)
		 VALUES (NULL, $1, $2, 'contact', 'Website contact form', $3) RETURNING id`,
		req.Name, req.Email, req.Message,
	).Scan(&id)
	if err != nil {
		httputil.Error(w, "could not save your message", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, map[string]string{"id": id, "status": "received"})
}
