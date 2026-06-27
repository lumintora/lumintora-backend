package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"

	"golang.org/x/crypto/bcrypt"
)

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
	if req.Email == "" || req.Password == "" || req.Name == "" {
		httputil.Error(w, "email, name and password are required", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var user models.User
	err = h.db.QueryRowContext(r.Context(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, $2, $3)
		 RETURNING id, email, name, xp, streak, created_at`,
		req.Email, req.Name, string(hash),
	).Scan(&user.ID, &user.Email, &user.Name, &user.XP, &user.Streak, &user.CreatedAt)
	if err != nil {
		httputil.Error(w, "email already in use", http.StatusConflict)
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
		`SELECT id, email, name, password_hash, xp, streak, created_at FROM users WHERE email=$1`,
		req.Email,
	).Scan(&user.ID, &user.Email, &user.Name, &hash, &user.XP, &user.Streak, &user.CreatedAt)
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
