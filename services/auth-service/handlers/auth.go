package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"
	"lumintora/pkg/tenant"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,30}$`)

type AuthHandler struct {
	db *sql.DB
}

func NewAuthHandler(db *sql.DB) *AuthHandler {
	return &AuthHandler{db: db}
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req models.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Email == "" || req.Password == "" || req.Name == "" || req.Username == "" {
		httputil.Error(w, "email, name, username and password are required", http.StatusBadRequest)
		return
	}
	if !usernameRE.MatchString(req.Username) {
		httputil.Error(w, "username must be 3–30 characters and may only contain letters, numbers, - and _", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var user models.User
	err = h.db.QueryRowContext(r.Context(),
		`INSERT INTO users (email, name, username, password_hash) VALUES ($1, $2, $3, $4)
		 RETURNING id, email, name, username, xp, streak, created_at`,
		req.Email, req.Name, req.Username, string(hash),
	).Scan(&user.ID, &user.Email, &user.Name, &user.Username, &user.XP, &user.Streak, &user.CreatedAt)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			if strings.Contains(pqErr.Constraint, "username") || strings.Contains(pqErr.Message, "username") {
				httputil.Error(w, "username already taken", http.StatusConflict)
			} else {
				httputil.Error(w, "email already in use", http.StatusConflict)
			}
			return
		}
		httputil.Error(w, "could not create account", http.StatusInternalServerError)
		return
	}

	// Provision the user's private tenant schema (idempotent).
	if _, err := h.db.ExecContext(r.Context(), tenant.CreateSchemaSQL, user.ID); err != nil {
		httputil.Error(w, "could not initialize account workspace", http.StatusInternalServerError)
		return
	}

	token, err := middleware.GenerateToken(user.ID, user.Email)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, models.AuthResponse{Token: token, User: user})
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var user models.User
	var hash string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, email, name, COALESCE(username, ''), password_hash, xp, streak, created_at FROM users WHERE email=$1`,
		req.Email,
	).Scan(&user.ID, &user.Email, &user.Name, &user.Username, &hash, &user.XP, &user.Streak, &user.CreatedAt)
	if err != nil {
		httputil.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		httputil.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	h.db.ExecContext(r.Context(),
		`UPDATE users SET
		   streak = CASE
		     WHEN last_active_at IS NULL THEN 1
		     WHEN last_active_at::date = NOW()::date THEN streak
		     WHEN last_active_at::date = NOW()::date - INTERVAL '1 day' THEN streak + 1
		     ELSE 1
		   END,
		   last_active_at = NOW()
		 WHERE id=$1`, user.ID)
	h.db.QueryRowContext(r.Context(), `SELECT xp, streak FROM users WHERE id=$1`, user.ID).Scan(&user.XP, &user.Streak)

	token, err := middleware.GenerateToken(user.ID, user.Email)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, models.AuthResponse{Token: token, User: user})
}
