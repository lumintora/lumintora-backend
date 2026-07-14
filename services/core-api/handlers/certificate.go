package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
)

type CertificateHandler struct{ db *sql.DB }

func NewCertificateHandler(db *sql.DB) *CertificateHandler {
	return &CertificateHandler{db: db}
}

type certResponse struct {
	CertID    string    `json:"cert_id"`
	PathTitle string    `json:"path_title"`
	UserName  string    `json:"user_name"`
	IssuedAt  time.Time `json:"issued_at"`
	Valid     bool      `json:"valid,omitempty"`
}

func genCertID(year, month int) (string, error) {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		if err != nil {
			return "", err
		}
		b[i] = chars[n.Int64()]
	}
	return fmt.Sprintf("LMT%d%02d-%s", year, month, string(b)), nil
}

// Issue issues (or returns the existing) certificate for a completed path.
// POST /api/v1/certificates  — requires JWT
func (h *CertificateHandler) Issue(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)

	var req struct {
		PathID string `json:"path_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.PathID) == "" {
		httputil.Error(w, "path_id required", http.StatusBadRequest)
		return
	}

	// Verify path belongs to this user and is 100% complete
	var progress int
	var pathTitle string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT progress, title FROM learning_paths WHERE id=$1 AND user_id=$2`,
		req.PathID, userID,
	).Scan(&progress, &pathTitle)
	if err != nil {
		httputil.Error(w, "path not found", http.StatusNotFound)
		return
	}
	if progress < 100 {
		httputil.Error(w, "path not completed yet", http.StatusBadRequest)
		return
	}

	var userName string
	h.db.QueryRowContext(r.Context(), `SELECT name FROM users WHERE id=$1`, userID).Scan(&userName)

	// Idempotent: return existing certificate for this (user, path) pair
	var existing certResponse
	err = h.db.QueryRowContext(r.Context(),
		`SELECT cert_id, path_title, user_name, issued_at
		 FROM certificates WHERE user_id=$1 AND path_id=$2`,
		userID, req.PathID,
	).Scan(&existing.CertID, &existing.PathTitle, &existing.UserName, &existing.IssuedAt)
	if err == nil {
		httputil.OK(w, existing)
		return
	}

	// Generate a collision-free cert ID
	now := time.Now()
	var certID string
	for i := 0; i < 5; i++ {
		candidate, err := genCertID(now.Year(), int(now.Month()))
		if err != nil {
			continue
		}
		var exists bool
		h.db.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM certificates WHERE cert_id=$1)`, candidate,
		).Scan(&exists)
		if !exists {
			certID = candidate
			break
		}
	}
	if certID == "" {
		httputil.Error(w, "could not generate certificate id", http.StatusInternalServerError)
		return
	}

	var result certResponse
	result.PathTitle = pathTitle
	result.UserName = userName
	// ON CONFLICT handles the rare race where two requests arrive simultaneously
	err = h.db.QueryRowContext(r.Context(),
		`INSERT INTO certificates (cert_id, user_id, path_id, path_title, user_name)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, path_id) WHERE path_id IS NOT NULL
		 DO UPDATE SET cert_id = certificates.cert_id
		 RETURNING cert_id, issued_at`,
		certID, userID, req.PathID, pathTitle, userName,
	).Scan(&result.CertID, &result.IssuedAt)
	if err != nil {
		httputil.Error(w, "could not issue certificate", http.StatusInternalServerError)
		return
	}

	httputil.OK(w, result)
}

// Verify looks up a certificate by its public cert_id.
// GET /api/v1/certificates/verify?cert=CERTID  — public, no auth
func (h *CertificateHandler) Verify(w http.ResponseWriter, r *http.Request) {
	certID := strings.TrimSpace(r.URL.Query().Get("cert"))
	if certID == "" {
		httputil.Error(w, "cert param required", http.StatusBadRequest)
		return
	}

	var result certResponse
	err := h.db.QueryRowContext(r.Context(),
		`SELECT cert_id, path_title, user_name, issued_at FROM certificates WHERE cert_id=$1`,
		certID,
	).Scan(&result.CertID, &result.PathTitle, &result.UserName, &result.IssuedAt)
	if err != nil {
		httputil.Error(w, "certificate not found", http.StatusNotFound)
		return
	}
	result.Valid = true
	httputil.OK(w, result)
}
