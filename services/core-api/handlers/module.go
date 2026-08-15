package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"
	"lumintora/pkg/tenant"

	"github.com/go-chi/chi/v5"
)

type ModuleHandler struct {
	db *sql.DB
}

func NewModuleHandler(db *sql.DB) *ModuleHandler {
	return &ModuleHandler{db: db}
}

func (h *ModuleHandler) List(w http.ResponseWriter, r *http.Request) {
	pathID := chi.URLParam(r, "pathID")
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		fmt.Sprintf(`SELECT m.id, m.path_id, m.title, m.description, m.type, m.order_index,
		        m.duration_minutes, m.xp_reward, m.difficulty, m.created_at,
		        COALESCE(ump.status, 'locked') as status
		 FROM %[1]s.modules m
		 JOIN %[1]s.learning_paths lp ON lp.id=m.path_id AND lp.user_id=$2
		 LEFT JOIN %[1]s.user_module_progress ump ON ump.module_id=m.id AND ump.user_id=$2
		 WHERE m.path_id=$1 ORDER BY m.order_index`, sc),
		pathID, userID,
	)
	if err != nil {
		httputil.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	modules := []models.Module{}
	for rows.Next() {
		var m models.Module
		if err := rows.Scan(
			&m.ID, &m.PathID, &m.Title, &m.Description, &m.Type, &m.OrderIndex,
			&m.DurationMinutes, &m.XPReward, &m.Difficulty, &m.CreatedAt, &m.Status,
		); err == nil {
			modules = append(modules, m)
		}
	}
	httputil.OK(w, modules)
}

func (h *ModuleHandler) Get(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var m models.Module
	err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT m.id, m.path_id, m.title, m.description, m.content, m.type, m.order_index,
		        m.duration_minutes, m.xp_reward, m.difficulty, m.created_at,
		        COALESCE(ump.status, 'locked') as status
		 FROM %[1]s.modules m
		 JOIN %[1]s.learning_paths lp ON lp.id=m.path_id AND lp.user_id=$2
		 LEFT JOIN %[1]s.user_module_progress ump ON ump.module_id=m.id AND ump.user_id=$2
		 WHERE m.id=$1`, sc),
		moduleID, userID,
	).Scan(
		&m.ID, &m.PathID, &m.Title, &m.Description, &m.Content, &m.Type, &m.OrderIndex,
		&m.DurationMinutes, &m.XPReward, &m.Difficulty, &m.CreatedAt, &m.Status,
	)
	if err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}
	httputil.OK(w, m)
}

func (h *ModuleHandler) Start(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var pathID string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT m.path_id FROM %[1]s.modules m
		 JOIN %[1]s.learning_paths lp ON lp.id=m.path_id
		 WHERE m.id=$1 AND lp.user_id=$2`, sc), moduleID, userID,
	).Scan(&pathID); err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}

	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.user_module_progress (user_id, module_id, path_id, status, started_at)
		 VALUES ($1, $2, $3, 'in_progress', NOW())
		 ON CONFLICT (user_id, module_id) DO UPDATE SET status='in_progress', started_at=NOW()`, sc),
		userID, moduleID, pathID,
	)
	httputil.OK(w, map[string]string{"status": "started"})
}

func (h *ModuleHandler) Feedback(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	switch req.Feedback {
	case "easy", "good", "hard":
	default:
		httputil.Error(w, "feedback must be easy, good, or hard", http.StatusBadRequest)
		return
	}

	var pathID string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT m.path_id FROM %[1]s.modules m
		 JOIN %[1]s.learning_paths lp ON lp.id=m.path_id
		 WHERE m.id=$1 AND lp.user_id=$2`, sc), moduleID, userID,
	).Scan(&pathID); err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}
	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.user_module_progress (user_id, module_id, path_id, status, difficulty_feedback)
		 VALUES ($1, $2, $3, 'in_progress', $4)
		 ON CONFLICT (user_id, module_id) DO UPDATE SET difficulty_feedback=EXCLUDED.difficulty_feedback`, sc),
		userID, moduleID, pathID, req.Feedback,
	)
	httputil.OK(w, map[string]string{"status": "recorded"})
}

func (h *ModuleHandler) Complete(w http.ResponseWriter, r *http.Request) {
	moduleID := chi.URLParam(r, "moduleID")
	userID := middleware.GetUserID(r)
	sc, ok := tenant.Schema(r.Context(), h.db, userID)
	if !ok {
		httputil.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var xpReward, orderIndex int
	var pathID string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT m.xp_reward, m.path_id, m.order_index FROM %[1]s.modules m
		 JOIN %[1]s.learning_paths lp ON lp.id=m.path_id
		 WHERE m.id=$1 AND lp.user_id=$2`, sc), moduleID, userID,
	).Scan(&xpReward, &pathID, &orderIndex); err != nil {
		httputil.Error(w, "module not found", http.StatusNotFound)
		return
	}

	var alreadyCompleted bool
	h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %[1]s.user_module_progress
		               WHERE user_id=$1 AND module_id=$2 AND status='completed')`, sc),
		userID, moduleID,
	).Scan(&alreadyCompleted)

	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`INSERT INTO %[1]s.user_module_progress (user_id, module_id, path_id, status, completed_at)
		 VALUES ($1, $2, $3, 'completed', NOW())
		 ON CONFLICT (user_id, module_id) DO UPDATE SET status='completed', completed_at=NOW()`, sc),
		userID, moduleID, pathID,
	)

	awarded := 0
	if !alreadyCompleted {
		awarded = xpReward
		h.db.ExecContext(r.Context(), `UPDATE users SET xp=xp+$1 WHERE id=$2`, xpReward, userID)
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`INSERT INTO %[1]s.xp_transactions (user_id, amount, reason, module_id) VALUES ($1,$2,'module_complete',$3)`, sc),
			userID, xpReward, moduleID,
		)
	}

	var nextID string
	if err := h.db.QueryRowContext(r.Context(),
		fmt.Sprintf(`SELECT id FROM %[1]s.modules WHERE path_id=$1 AND order_index>$2 ORDER BY order_index ASC LIMIT 1`, sc),
		pathID, orderIndex,
	).Scan(&nextID); err == nil && nextID != "" {
		h.db.ExecContext(r.Context(),
			fmt.Sprintf(`INSERT INTO %[1]s.user_module_progress (user_id, module_id, path_id, status)
			 VALUES ($1, $2, $3, 'available')
			 ON CONFLICT (user_id, module_id) DO UPDATE SET status=
			   CASE WHEN user_module_progress.status IN ('locked','not_started')
			        THEN 'available' ELSE user_module_progress.status END`, sc),
			userID, nextID, pathID,
		)
	}

	h.db.ExecContext(r.Context(),
		fmt.Sprintf(`UPDATE %[1]s.learning_paths SET
		   completed_modules=(SELECT COUNT(*) FROM %[1]s.user_module_progress WHERE path_id=$1 AND user_id=$2 AND status='completed'),
		   progress=LEAST(100, (SELECT COUNT(*)*100/GREATEST(total_modules,1) FROM %[1]s.user_module_progress WHERE path_id=$1 AND user_id=$2 AND status='completed')),
		   status=CASE WHEN (SELECT COUNT(*) FROM %[1]s.user_module_progress WHERE path_id=$1 AND user_id=$2 AND status='completed') >= total_modules
		               THEN 'completed' ELSE 'active' END,
		   updated_at=NOW()
		 WHERE id=$1 AND user_id=$2`, sc),
		pathID, userID,
	)

	var xp int
	h.db.QueryRowContext(r.Context(), `SELECT xp FROM users WHERE id=$1`, userID).Scan(&xp)
	httputil.OK(w, map[string]interface{}{
		"xp_earned":         awarded,
		"total_xp":          xp,
		"already_completed": alreadyCompleted,
		"next_module_id":    nextID,
	})
}
