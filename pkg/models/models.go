package models

import "time"

type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	AvatarURL    *string    `json:"avatar_url,omitempty"`
	Phone        *string    `json:"phone,omitempty"`
	Bio          *string    `json:"bio,omitempty"`
	Location     *string    `json:"location,omitempty"`
	Website      *string    `json:"website,omitempty"`
	Twitter      *string    `json:"twitter,omitempty"`
	Github       *string    `json:"github,omitempty"`
	Linkedin     *string    `json:"linkedin,omitempty"`
	XP           int        `json:"xp"`
	Streak       int        `json:"streak"`
	LastActiveAt *time.Time `json:"last_active_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type ActivityDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
	XP    int    `json:"xp"`
}

type ActivitySummary struct {
	Days               []ActivityDay `json:"days"`
	TotalActiveDays    int           `json:"total_active_days"`
	TotalContributions int           `json:"total_contributions"`
	LongestStreak      int           `json:"longest_streak"`
	CurrentStreak      int           `json:"current_streak"`
}

type LearningPath struct {
	ID               string    `json:"id"`
	UserID           string    `json:"user_id"`
	Title            string    `json:"title"`
	Description      string    `json:"description"`
	Goal             string    `json:"goal"`
	Topic            string    `json:"topic"`
	Level            string    `json:"level"`
	Status           string    `json:"status"`
	Progress         int       `json:"progress"`
	TotalModules     int       `json:"total_modules"`
	CompletedModules int       `json:"completed_modules"`
	EstimatedHours   int       `json:"estimated_hours"`
	Tags             []string  `json:"tags"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Modules          []Module  `json:"modules,omitempty"`
}

type Module struct {
	ID              string    `json:"id"`
	PathID          string    `json:"path_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Content         string    `json:"content,omitempty"`
	Type            string    `json:"type"`
	OrderIndex      int       `json:"order_index"`
	DurationMinutes int       `json:"duration_minutes"`
	XPReward        int       `json:"xp_reward"`
	Status          string    `json:"status"`
	Difficulty      string    `json:"difficulty"`
	Source          string    `json:"source,omitempty"`
	AdaptiveReason  string    `json:"adaptive_reason,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

type QuizQuestion struct {
	ID            string   `json:"id"`
	ModuleID      string   `json:"module_id"`
	Question      string   `json:"question"`
	Options       []string `json:"options"`
	CorrectOption int      `json:"correct_option,omitempty"`
	Explanation   string   `json:"explanation,omitempty"`
	OrderIndex    int      `json:"order_index"`
}

type UserModuleProgress struct {
	ID               string     `json:"id"`
	UserID           string     `json:"user_id"`
	ModuleID         string     `json:"module_id"`
	PathID           string     `json:"path_id"`
	Status           string     `json:"status"`
	Score            int        `json:"score"`
	TimeSpentSeconds int        `json:"time_spent_seconds"`
	Attempts         int        `json:"attempts"`
	CompletedAt      *time.Time `json:"completed_at,omitempty"`
	StartedAt        *time.Time `json:"started_at,omitempty"`
}

type LeaderboardEntry struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	AvatarURL *string `json:"avatar_url,omitempty"`
	XP        int     `json:"xp"`
	Streak    int     `json:"streak"`
	Rank      int     `json:"rank"`
}

type RegisterRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type CreatePathRequest struct {
	Goal  string `json:"goal"`
	Level string `json:"level"`
}

type GeneratePathRequest struct {
	Goal  string `json:"goal"`
	Level string `json:"level"`
	Topic string `json:"topic"`
	Model string `json:"model,omitempty"`
}

type ExplainRequest struct {
	Concept  string `json:"concept"`
	Context  string `json:"context,omitempty"`
	ModuleID string `json:"module_id,omitempty"`
}

type HintRequest struct {
	Problem  string `json:"problem"`
	Attempt  string `json:"attempt,omitempty"`
	ModuleID string `json:"module_id,omitempty"`
}

type EvalCodeRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	Problem  string `json:"problem"`
}

type QuizSubmitRequest struct {
	Answers []int `json:"answers"`
}

type WaitlistRequest struct {
	Email string `json:"email"`
}
