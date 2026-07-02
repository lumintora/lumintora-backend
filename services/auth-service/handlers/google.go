package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"

	"github.com/golang-jwt/jwt/v5"
)

type GoogleHandler struct {
	db           *sql.DB
	clientID     string
	clientSecret string
	redirectURI  string
	frontendURL  string
}

func NewGoogleHandler(db *sql.DB) *GoogleHandler {
	return &GoogleHandler{
		db:           db,
		clientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		clientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		redirectURI:  gEnv("GOOGLE_REDIRECT_URI", "https://lumintora-api.onrender.com/api/v1/auth/google/callback"),
		frontendURL:  gEnv("FRONTEND_URL", "https://lumintora.in"),
	}
}

// Redirect sends the user to Google's consent screen.
func (h *GoogleHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	if h.clientID == "" {
		http.Error(w, "Google OAuth not configured", http.StatusServiceUnavailable)
		return
	}
	params := url.Values{
		"client_id":     {h.clientID},
		"redirect_uri":  {h.redirectURI},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"access_type":   {"offline"},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+params.Encode(), http.StatusTemporaryRedirect)
}

// Callback handles the OAuth code exchange from Google.
func (h *GoogleHandler) Callback(w http.ResponseWriter, r *http.Request) {
	errParam := r.URL.Query().Get("error")
	code := r.URL.Query().Get("code")
	if errParam != "" || code == "" {
		http.Redirect(w, r, h.frontendURL+"/login?error=google_cancelled", http.StatusTemporaryRedirect)
		return
	}

	gUser, err := h.fetchGoogleUser(code)
	if err != nil {
		http.Redirect(w, r, h.frontendURL+"/login?error=google_failed", http.StatusTemporaryRedirect)
		return
	}

	// Look up existing user by email or google_id
	var userID, email string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id, email FROM users WHERE email=$1 OR google_id=$2 LIMIT 1`,
		gUser.Email, gUser.ID,
	).Scan(&userID, &email)

	if err == sql.ErrNoRows {
		// Brand-new user — redirect to complete-profile page
		pending, _ := h.issuePending(gUser)
		dest := fmt.Sprintf("%s/auth/complete?pending=%s&name=%s&email=%s",
			h.frontendURL,
			url.QueryEscape(pending),
			url.QueryEscape(gUser.Name),
			url.QueryEscape(gUser.Email),
		)
		http.Redirect(w, r, dest, http.StatusTemporaryRedirect)
		return
	}
	if err != nil {
		http.Redirect(w, r, h.frontendURL+"/login?error=db_error", http.StatusTemporaryRedirect)
		return
	}

	// Existing user — update google_id / picture, then issue JWT
	h.db.ExecContext(r.Context(),
		`UPDATE users SET google_id=$1, picture_url=$2, updated_at=NOW() WHERE id=$3`,
		gUser.ID, gUser.Picture, userID)

	token, err := middleware.GenerateToken(userID, email)
	if err != nil {
		http.Redirect(w, r, h.frontendURL+"/login?error=token_error", http.StatusTemporaryRedirect)
		return
	}
	http.Redirect(w, r, h.frontendURL+"/auth/success?token="+url.QueryEscape(token), http.StatusTemporaryRedirect)
}

// Complete creates the account for a new Google user after they pick a username.
func (h *GoogleHandler) Complete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PendingToken string `json:"pending_token"`
		Username     string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(req.Username)
	if !usernameRE.MatchString(username) {
		httputil.Error(w, "username must be 3-30 characters: letters, numbers, - and _ only", http.StatusBadRequest)
		return
	}

	gUser, err := h.verifyPending(req.PendingToken)
	if err != nil {
		httputil.Error(w, "session expired — please try Google sign-in again", http.StatusUnauthorized)
		return
	}

	var userID string
	err = h.db.QueryRowContext(r.Context(),
		`INSERT INTO users (name, email, username, google_id, picture_url, password_hash)
		 VALUES ($1,$2,$3,$4,$5,'') RETURNING id`,
		gUser.Name, gUser.Email, username, gUser.ID, gUser.Picture,
	).Scan(&userID)
	if err != nil {
		msg := "could not create account"
		if strings.Contains(err.Error(), "username") {
			msg = "username already taken"
		} else if strings.Contains(err.Error(), "email") {
			msg = "an account with this email already exists — sign in instead"
		}
		httputil.Error(w, msg, http.StatusConflict)
		return
	}

	token, err := middleware.GenerateToken(userID, gUser.Email)
	if err != nil {
		httputil.Error(w, "could not issue token", http.StatusInternalServerError)
		return
	}
	httputil.OK(w, map[string]string{"token": token})
}

// ── internal helpers ─────────────────────────────────────────────────

type googleUserInfo struct {
	ID      string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (h *GoogleHandler) fetchGoogleUser(code string) (*googleUserInfo, error) {
	// Exchange code for access token
	resp, err := http.PostForm("https://oauth2.googleapis.com/token", url.Values{
		"code":          {code},
		"client_id":     {h.clientID},
		"client_secret": {h.clientSecret},
		"redirect_uri":  {h.redirectURI},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return nil, fmt.Errorf("token exchange failed")
	}

	// Fetch user profile
	req, _ := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uResp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer uResp.Body.Close()
	var g googleUserInfo
	return &g, json.NewDecoder(uResp.Body).Decode(&g)
}

// pendingClaims is a short-lived JWT carrying Google profile data for new users.
type pendingClaims struct {
	Kind     string `json:"kind"` // "google_pending"
	GoogleID string `json:"gid"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Picture  string `json:"pic"`
	jwt.RegisteredClaims
}

func (h *GoogleHandler) issuePending(g *googleUserInfo) (string, error) {
	claims := pendingClaims{
		Kind:     "google_pending",
		GoogleID: g.ID,
		Email:    g.Email,
		Name:     g.Name,
		Picture:  g.Picture,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(15 * time.Minute)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(googleJWTKey())
}

func (h *GoogleHandler) verifyPending(raw string) (*googleUserInfo, error) {
	tok, err := jwt.ParseWithClaims(raw, &pendingClaims{}, func(t *jwt.Token) (interface{}, error) {
		return googleJWTKey(), nil
	})
	if err != nil || !tok.Valid {
		return nil, fmt.Errorf("invalid pending token")
	}
	c, ok := tok.Claims.(*pendingClaims)
	if !ok || c.Kind != "google_pending" {
		return nil, fmt.Errorf("wrong token kind")
	}
	return &googleUserInfo{ID: c.GoogleID, Email: c.Email, Name: c.Name, Picture: c.Picture}, nil
}

func googleJWTKey() []byte {
	if s := os.Getenv("JWT_SECRET"); s != "" {
		return []byte(s)
	}
	return []byte("default_dev_secret")
}

func gEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
