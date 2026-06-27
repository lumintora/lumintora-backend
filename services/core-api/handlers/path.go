package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"lumintora/pkg/httputil"
	"lumintora/pkg/middleware"
	"lumintora/pkg/models"

	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"
)

type PathHandler struct {
	db *sql.DB
}

func NewPathHandler(db *sql.DB) *PathHandler {
	return &PathHandler{db: db}
}

func (h *PathHandler) List(w http.ResponseWriter, r *http.Request) {
	userID := middleware.GetUserID(r)
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, user_id, title, description, goal, topic, level, status,
		        progress, total_modules, completed_modules, estimated_hours, tags, created_at, updated_at
		 FROM learning_paths WHERE user_id=$1 ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		httputil.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	paths := []models.LearningPath{}
	for rows.Next() {
		var p models.LearningPath
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Title, &p.Description, &p.Goal, &p.Topic, &p.Level, &p.Status,
			&p.Progress, &p.TotalModules, &p.CompletedModules, &p.EstimatedHours, pq.Array(&p.Tags),
			&p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			continue
		}
		paths = append(paths, p)
	}
	httputil.OK(w, paths)
}

func (h *PathHandler) Get(w http.ResponseWriter, r *http.Request) {
	pathID := chi.URLParam(r, "pathID")
	userID := middleware.GetUserID(r)

	var p models.LearningPath
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, user_id, title, description, goal, topic, level, status,
		        progress, total_modules, completed_modules, estimated_hours, tags, created_at, updated_at
		 FROM learning_paths WHERE id=$1 AND user_id=$2`,
		pathID, userID,
	).Scan(
		&p.ID, &p.UserID, &p.Title, &p.Description, &p.Goal, &p.Topic, &p.Level, &p.Status,
		&p.Progress, &p.TotalModules, &p.CompletedModules, &p.EstimatedHours, pq.Array(&p.Tags),
		&p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		httputil.Error(w, "path not found", http.StatusNotFound)
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT m.id, m.path_id, m.title, m.description, m.type, m.order_index,
		        m.duration_minutes, m.xp_reward, m.difficulty, m.created_at,
		        COALESCE(m.source,'initial'), COALESCE(m.adaptive_reason,''),
		        COALESCE(ump.status, 'locked') as status
		 FROM modules m
		 LEFT JOIN user_module_progress ump ON ump.module_id=m.id AND ump.user_id=$2
		 WHERE m.path_id=$1 ORDER BY m.order_index`,
		pathID, userID,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var mod models.Module
			if err := rows.Scan(
				&mod.ID, &mod.PathID, &mod.Title, &mod.Description, &mod.Type, &mod.OrderIndex,
				&mod.DurationMinutes, &mod.XPReward, &mod.Difficulty, &mod.CreatedAt,
				&mod.Source, &mod.AdaptiveReason, &mod.Status,
			); err == nil {
				p.Modules = append(p.Modules, mod)
			}
		}
	}

	httputil.OK(w, p)
}

func (h *PathHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req models.CreatePathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	userID := middleware.GetUserID(r)

	var p models.LearningPath
	err := h.db.QueryRowContext(r.Context(),
		`INSERT INTO learning_paths (user_id, title, description, goal, topic, level, estimated_hours)
		 VALUES ($1, $2, $3, $4, $5, $6, 10)
		 RETURNING id, user_id, title, description, goal, topic, level, status, progress,
		           total_modules, completed_modules, estimated_hours, created_at, updated_at`,
		userID, req.Goal, "AI-generated learning path", req.Goal, req.Goal, req.Level,
	).Scan(
		&p.ID, &p.UserID, &p.Title, &p.Description, &p.Goal, &p.Topic, &p.Level, &p.Status,
		&p.Progress, &p.TotalModules, &p.CompletedModules, &p.EstimatedHours, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		httputil.Error(w, "could not create path", http.StatusInternalServerError)
		return
	}

	httputil.Created(w, p)
}

func (h *PathHandler) Delete(w http.ResponseWriter, r *http.Request) {
	pathID := chi.URLParam(r, "pathID")
	userID := middleware.GetUserID(r)
	h.db.ExecContext(r.Context(), `DELETE FROM learning_paths WHERE id=$1 AND user_id=$2`, pathID, userID)
	w.WriteHeader(http.StatusNoContent)
}
