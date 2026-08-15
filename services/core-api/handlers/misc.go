package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"
	"lumintora/pkg/tenant"

	"github.com/lib/pq"
)

// ── UserHandler ──────────────────────────────────────────────────────────────

type UserHandler struct{ db *sql.DB }

func NewUserHandler(db *sql.DB) *UserHandler { return &UserHandler{db: db} }

const userColumns = `id, email, name, COALESCE(username,''), avatar_url, phone, bio, location, website,
	twitter, github, linkedin, xp, streak, last_active_at, created_at`

func scanUser(row interface{ Scan(...interface{}) error }, u *models.User) error {
	return row.Scan(&u.ID, &u.Email, &u.Name, &u.Username, &u.AvatarURL, &u.Phone, &u.Bio,
		&u.Location, &u.Website, &u.Twitter, &u.Github, &u.Linkedin,
		&u.XP, &u.Streak, &u.LastActiveAt, &u.CreatedAt)
}

func (h *UserHandler) Me(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	var u models.User
	err := scanUser(h.db.QueryRowContext(r.Context(),
		`SELECT `+userColumns+` FROM users WHERE id=$1`, userID), &u)
	if err != nil {
		httputil.Error(w, "user not found", http.StatusNotFound)
		return
	}
	httputil.OK(w, u)
}

func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)

	var req struct {
		Name      *string `json:"name"`
		Username  *string `json:"username"`
		Email     *string `json:"email"`
		AvatarURL *string `json:"avatar_url"`
		Phone     *string `json:"phone"`
		Bio       *string `json:"bio"`
		Location  *string `json:"location"`
		Website   *string `json:"website"`
		Twitter   *string `json:"twitter"`
		Github    *string `json:"github"`
		Linkedin  *string `json:"linkedin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		httputil.Error(w, "name cannot be empty", http.StatusBadRequest)
		return
	}
	if req.Username != nil {
		u := strings.ToLower(strings.TrimSpace(*req.Username))
		if u != "" {
			matched, _ := regexp.MatchString(`^[a-zA-Z0-9_-]{3,30}$`, u)
			if !matched {
				httputil.Error(w, "username must be 3–30 characters and may only contain letters, numbers, - and _", http.StatusBadRequest)
				return
			}
		}
		req.Username = &u
	}
	if req.Email != nil {
		e := strings.TrimSpace(strings.ToLower(*req.Email))
		if e == "" || !strings.Contains(e, "@") {
			httputil.Error(w, "a valid email is required", http.StatusBadRequest)
			return
		}
		req.Email = &e
	}

	_, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET
		   name      = COALESCE($2, name),
		   username  = COALESCE(NULLIF($3,''), username),
		   email     = COALESCE($4, email),
		   avatar_url= $5,
		   phone     = $6,
		   bio       = $7,
		   location  = $8,
		   website   = $9,
		   twitter   = $10,
		   github    = $11,
		   linkedin  = $12,
		   updated_at= NOW()
		 WHERE id=$1`,
		userID,
		trimPtr(req.Name), nilIfEmpty(req.Username), req.Email,
		nilIfEmpty(req.AvatarURL), nilIfEmpty(req.Phone), nilIfEmpty(req.Bio),
		nilIfEmpty(req.Location), nilIfEmpty(req.Website), nilIfEmpty(stripAt(req.Twitter)),
		nilIfEmpty(stripAt(req.Github)), nilIfEmpty(req.Linkedin),
	)
	if err != nil {
		if pqErr, ok := err.(*pq.Error); ok && pqErr.Code == "23505" {
			if strings.Contains(pqErr.Constraint, "username") || strings.Contains(pqErr.Message, "username") {
				httputil.Error(w, "username already taken", http.StatusConflict)
			} else {
				httputil.Error(w, "that email is already in use", http.StatusConflict)
			}
			return
		}
		httputil.Error(w, "could not update profile", http.StatusInternalServerError)
		return
	}

	var u models.User
	if err := scanUser(h.db.QueryRowContext(r.Context(),
		`SELECT `+userColumns+` FROM users WHERE id=$1`, userID), &u); err != nil {
		httputil.Error(w, "user not found", http.StatusNotFound)
		return
	}
	httputil.OK(w, u)
}

func (h *UserHandler) Activity(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(userID)
	if !ok {
		httputil.OK(w, models.ActivitySummary{Days: []models.ActivityDay{}})
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT created_at::date AS day, COUNT(*) AS events, COALESCE(SUM(amount),0) AS xp
		 FROM %[1]s.xp_transactions
		 WHERE user_id=$1 AND created_at >= (NOW() - INTERVAL '371 days')::date
		 GROUP BY day ORDER BY day`, sc), userID)
	if err != nil {
		httputil.OK(w, models.ActivitySummary{Days: []models.ActivityDay{}})
		return
	}
	defer rows.Close()

	out := models.ActivitySummary{Days: []models.ActivityDay{}}
	active := map[string]bool{}
	for rows.Next() {
		var day time.Time
		var d models.ActivityDay
		if err := rows.Scan(&day, &d.Count, &d.XP); err != nil {
			continue
		}
		d.Date = day.Format("2006-01-02")
		out.Days = append(out.Days, d)
		out.TotalContributions += d.Count
		out.TotalActiveDays++
		active[d.Date] = true
	}

	out.LongestStreak, out.CurrentStreak = streaks(active)
	httputil.OK(w, out)
}

func streaks(active map[string]bool) (longest, current int) {
	if len(active) == 0 {
		return 0, 0
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)

	start := today
	if !active[start.Format("2006-01-02")] {
		start = start.AddDate(0, 0, -1)
	}
	for active[start.Format("2006-01-02")] {
		current++
		start = start.AddDate(0, 0, -1)
	}

	run := 0
	for d := today.AddDate(0, 0, -370); !d.After(today); d = d.AddDate(0, 0, 1) {
		if active[d.Format("2006-01-02")] {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	return longest, current
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func nilIfEmpty(s *string) interface{} {
	if s == nil {
		return nil
	}
	if t := strings.TrimSpace(*s); t != "" {
		return t
	}
	return nil
}

func stripAt(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimPrefix(strings.TrimSpace(*s), "@")
	return &t
}

// ── WaitlistHandler ──────────────────────────────────────────────────────────

type WaitlistHandler struct{ db *sql.DB }

func NewWaitlistHandler(db *sql.DB) *WaitlistHandler { return &WaitlistHandler{db: db} }

func (h *WaitlistHandler) Join(w http.ResponseWriter, r *http.Request) {
	var req models.WaitlistRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.Email == "" {
		httputil.Error(w, "email required", http.StatusBadRequest)
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO waitlist (email) VALUES ($1) ON CONFLICT DO NOTHING`, req.Email,
	)
	if err != nil {
		httputil.Error(w, "could not join waitlist", http.StatusInternalServerError)
		return
	}
	httputil.OK(w, map[string]string{"status": "joined"})
}

// ── LeaderboardHandler ───────────────────────────────────────────────────────

type LeaderboardHandler struct{ db *sql.DB }

func NewLeaderboardHandler(db *sql.DB) *LeaderboardHandler { return &LeaderboardHandler{db: db} }

func (h *LeaderboardHandler) Get(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, username, avatar_url, xp, streak, rank FROM leaderboard LIMIT 50`,
	)
	if err != nil {
		httputil.OK(w, []models.LeaderboardEntry{})
		return
	}
	defer rows.Close()

	entries := []models.LeaderboardEntry{}
	for rows.Next() {
		var e models.LeaderboardEntry
		rows.Scan(&e.ID, &e.Name, &e.Username, &e.AvatarURL, &e.XP, &e.Streak, &e.Rank)
		entries = append(entries, e)
	}
	httputil.OK(w, entries)
}
