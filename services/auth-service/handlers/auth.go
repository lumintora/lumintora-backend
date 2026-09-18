package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/mailer"
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

	// Welcome email (best-effort — never blocks or fails registration).
	go sendWelcomeEmail(user.Email, user.Name)

	token, err := middleware.GenerateToken(user.ID, user.Email)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, models.AuthResponse{Token: token, User: user})
}

// sendWelcomeEmail sends a one-time welcome message on successful signup. It is
// a no-op (logged) when no email provider is configured.
func sendWelcomeEmail(email, name string) {
	if !mailer.Configured() {
		return
	}
	frontend := envOr("FRONTEND_URL", "http://localhost:3000")
	body := fmt.Sprintf(`Hi %s,

Welcome to Lumintora! Your account is ready.

Jump in and try Forge — it finds what you've forgotten and rebuilds it with a short, adaptive refresh:
%s/forge

Happy learning,
— The Lumintora team`, firstName(name), frontend)
	if err := mailer.Send(email, "Welcome to Lumintora 🎉", body); err != nil {
		log.Printf("welcome email failed for %s: %v", email, err)
	} else {
		log.Printf("welcome email sent to %s via %s", email, mailer.Provider())
	}
}

// ForgotPassword issues a single-use 6-digit reset code for an email. It always
// responds 200 (never reveals whether the email exists). The code is emailed
// when SMTP is configured; otherwise it is returned in the response and logged
// so the flow works in local/dev without a mail server.
func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	resp := map[string]interface{}{
		"message": "If an account exists for that email, a reset code has been sent.",
	}
	if req.Email == "" {
		httputil.OK(w, resp)
		return
	}

	var userID, name string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, COALESCE(name,'') FROM users WHERE lower(email)=$1`, req.Email).Scan(&userID, &name)
	if err != nil {
		// Unknown email — respond the same way to avoid user enumeration.
		httputil.OK(w, resp)
		return
	}

	code := genCode()
	hash, _ := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)

	// Invalidate any prior unused codes, then store the new one (30-min expiry).
	h.db.ExecContext(r.Context(), `UPDATE password_resets SET used=true WHERE user_id=$1 AND used=false`, userID)
	if _, err := h.db.ExecContext(r.Context(),
		`INSERT INTO password_resets (user_id, code_hash, expires_at) VALUES ($1,$2,$3)`,
		userID, string(hash), time.Now().Add(30*time.Minute)); err != nil {
		httputil.Error(w, "could not start password reset", http.StatusInternalServerError)
		return
	}

	frontend := envOr("FRONTEND_URL", "http://localhost:3000")
	link := fmt.Sprintf("%s/reset-password?email=%s&code=%s", frontend, req.Email, code)
	body := fmt.Sprintf("Hi %s,\n\nUse this code to reset your Lumintora password:\n\n    %s\n\nOr open this link:\n%s\n\nThis code expires in 30 minutes. If you didn't request it, you can ignore this email.\n\n— Lumintora",
		firstName(name), code, link)

	if mailer.Configured() {
		if err := mailer.Send(req.Email, "Reset your Lumintora password", body); err != nil {
			log.Printf("password reset email failed for %s: %v", req.Email, err)
		} else {
			log.Printf("password reset email sent to %s via %s", req.Email, mailer.Provider())
		}
	} else {
		// Dev fallback: surface the code so the flow is usable without SMTP.
		log.Printf("[DEV] password reset code for %s: %s (link: %s)", req.Email, code, link)
		resp["dev_code"] = code
		resp["dev_link"] = link
	}
	httputil.OK(w, resp)
}

// ResetPassword verifies a code and sets a new password.
func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Code = strings.TrimSpace(req.Code)
	if len(req.Password) < 6 {
		httputil.Error(w, "password must be at least 6 characters", http.StatusBadRequest)
		return
	}

	var userID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM users WHERE lower(email)=$1`, req.Email).Scan(&userID); err != nil {
		httputil.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}

	// Find the most recent valid reset row and check the code.
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, code_hash FROM password_resets
		   WHERE user_id=$1 AND used=false AND expires_at > NOW()
		   ORDER BY created_at DESC LIMIT 5`, userID)
	if err != nil {
		httputil.Error(w, "could not verify code", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	matchedID := ""
	for rows.Next() {
		var id, hash string
		rows.Scan(&id, &hash)
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Code)) == nil {
			matchedID = id
			break
		}
	}
	if matchedID == "" {
		httputil.Error(w, "invalid or expired code", http.StatusBadRequest)
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		httputil.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.db.ExecContext(r.Context(), `UPDATE users SET password_hash=$1, updated_at=NOW() WHERE id=$2`, string(newHash), userID)
	h.db.ExecContext(r.Context(), `UPDATE password_resets SET used=true WHERE id=$1`, matchedID)

	httputil.OK(w, map[string]string{"message": "Password updated — you can now sign in."})
}

func genCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

func firstName(name string) string {
	if name == "" {
		return "there"
	}
	return strings.Fields(name)[0]
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
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
