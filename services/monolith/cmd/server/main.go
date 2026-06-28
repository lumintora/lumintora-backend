// Monolith entry point — combines auth-service, core-api, and ai-service into a
// single binary. Used for single-host deployments (Render free tier, etc.) while
// keeping the codebase organised as microservices for easy future splitting.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"lumintora/pkg/db"
	"lumintora/pkg/middleware"
	aih "lumintora/services/ai-service/handlers"
	authh "lumintora/services/auth-service/handlers"
	coreh "lumintora/services/core-api/handlers"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func main() {
	database, err := db.Connect()
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrations failed: %v", err)
	}
	log.Println("✅ database ready")

	// Handlers from all three services
	authHandler := authh.NewAuthHandler(database)
	pathHandler := coreh.NewPathHandler(database)
	moduleHandler := coreh.NewModuleHandler(database)
	userHandler := coreh.NewUserHandler(database)
	waitlistHandler := coreh.NewWaitlistHandler(database)
	leaderboardHandler := coreh.NewLeaderboardHandler(database)
	feedbackHandler := coreh.NewFeedbackHandler(database)
	contactHandler := coreh.NewContactHandler(database)
	aiHandler := aih.NewAIHandler(database)
	chatHandler := aih.NewChatHandler(aiHandler)
	execHandler := aih.NewExecHandler()
	quizHandler := aih.NewQuizHandler(database)

	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Compress(5))
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	allowedOrigins := map[string]bool{
		getEnv("CORS_ORIGIN", "http://localhost:3000"): true,
		"http://localhost:5173":                         true,
		"https://www.lumintora.in":                     true,
		"https://lumintora.in":                         true,
	}
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc: func(r *http.Request, origin string) bool {
			if allowedOrigins[origin] {
				return true
			}
			// Allow all Cloudflare Pages preview deployments.
			return strings.HasSuffix(origin, ".lumintora-frontend.pages.dev") ||
				strings.HasSuffix(origin, ".pages.dev")
		},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"lumintora-api"}`))
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Public
		r.Post("/auth/register", authHandler.Register)
		r.Post("/auth/login", authHandler.Login)
		r.Post("/waitlist", waitlistHandler.Join)
		r.Post("/contact", contactHandler.Submit)
		r.Post("/chat", chatHandler.Chat)
		r.Get("/leaderboard", leaderboardHandler.Get)

		// Protected
		r.Group(func(r chi.Router) {
			r.Use(middleware.JWTAuth)

			r.Post("/feedback", feedbackHandler.Submit)

			// User
			r.Get("/me", userHandler.Me)
			r.Patch("/me", userHandler.Update)
			r.Get("/me/activity", userHandler.Activity)

			// Paths
			r.Get("/paths", pathHandler.List)
			r.Post("/paths", pathHandler.Create)
			r.Get("/paths/{pathID}", pathHandler.Get)
			r.Delete("/paths/{pathID}", pathHandler.Delete)
			r.Post("/paths/{pathID}/adapt", aiHandler.Adapt)

			// Modules
			r.Get("/paths/{pathID}/modules", moduleHandler.List)
			r.Get("/modules/{moduleID}", moduleHandler.Get)
			r.Get("/modules/{moduleID}/content", aiHandler.GetContent)
			r.Post("/modules/{moduleID}/start", moduleHandler.Start)
			r.Post("/modules/{moduleID}/complete", moduleHandler.Complete)
			r.Post("/modules/{moduleID}/feedback", moduleHandler.Feedback)
			r.Get("/modules/{moduleID}/quiz", aiHandler.GetQuiz)
			r.Post("/modules/{moduleID}/quiz", quizHandler.SubmitQuiz)

			// AI
			r.Post("/ai/generate-path", aiHandler.GeneratePath)
			r.Post("/ai/explain", aiHandler.Explain)
			r.Post("/ai/hint", aiHandler.Hint)
			r.Post("/ai/evaluate-code", aiHandler.EvaluateCode)
			r.Post("/ai/generate-resume", aiHandler.GenerateResume)

			// Code execution
			r.Post("/execute", execHandler.Execute)
		})
	})

	port := getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 180 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("🚀 lumintora-api running on port %s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
