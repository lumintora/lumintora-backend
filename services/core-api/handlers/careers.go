package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"lumintora/pkg/httputil"
)

var validRoles = map[string]bool{"gtm": true, "qa": true}

type CareersHandler struct{ db *sql.DB }

func NewCareersHandler(db *sql.DB) *CareersHandler { return &CareersHandler{db: db} }

func (h *CareersHandler) Apply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string            `json:"name"`
		Email    string            `json:"email"`
		College  string            `json:"college"`
		Year     string            `json:"year"`
		LinkedIn string            `json:"linkedin"`
		Role     string            `json:"role"`
		Answers  map[string]string `json:"answers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.TrimSpace(req.Email)
	req.College = strings.TrimSpace(req.College)
	req.Year = strings.TrimSpace(req.Year)
	req.Role = strings.TrimSpace(strings.ToLower(req.Role))

	if req.Name == "" || req.Email == "" || req.College == "" || req.Year == "" {
		httputil.Error(w, "name, email, college and year are required", http.StatusBadRequest)
		return
	}
	if !validRoles[req.Role] {
		httputil.Error(w, "invalid role — must be gtm or qa", http.StatusBadRequest)
		return
	}

	answersJSON, _ := json.Marshal(req.Answers)

	var linkedin interface{}
	if l := strings.TrimSpace(req.LinkedIn); l != "" {
		linkedin = l
	}

	var id string
	err := h.db.QueryRowContext(r.Context(),
		`INSERT INTO career_applications (name, email, college, year, linkedin, role, answers)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		req.Name, req.Email, req.College, req.Year, linkedin, req.Role, answersJSON,
	).Scan(&id)
	if err != nil {
		httputil.Error(w, "could not save your application", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, map[string]string{"id": id, "status": "received"})
}
