package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"lumintora/pkg/db"
	"lumintora/pkg/middleware"
	"lumintora/services/ai-service/handlers"

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
	log.Println("✅ ai-service: database ready")

	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{getEnv("CORS_ORIGIN", "http://localhost:3000"), "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"ai-service"}`))
	})

	aiHandler := handlers.NewAIHandler(database)
	execHandler := handlers.NewExecHandler()
	quizHandler := handlers.NewQuizHandler(database)

	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.JWTAuth)

			// Module AI content and quiz
			r.Get("/modules/{moduleID}/content", aiHandler.GetContent)
			r.Get("/modules/{moduleID}/quiz", aiHandler.GetQuiz)
			r.Post("/modules/{moduleID}/quiz", quizHandler.SubmitQuiz)

			// Path adaptation
			r.Post("/paths/{pathID}/adapt", aiHandler.Adapt)

			// AI generation endpoints
			r.Post("/ai/generate-path", aiHandler.GeneratePath)
			r.Post("/ai/explain", aiHandler.Explain)
			r.Post("/ai/hint", aiHandler.Hint)
			r.Post("/ai/evaluate-code", aiHandler.EvaluateCode)
			r.Post("/ai/generate-resume", aiHandler.GenerateResume)

			// Code execution
			r.Post("/execute", execHandler.Execute)
		})
	})

	port := getEnv("PORT", "8083")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 180 * time.Second, // AI generation can take ~90s
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("🚀 ai-service running on port %s", port)
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
