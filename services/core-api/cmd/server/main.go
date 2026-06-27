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
	"lumintora/services/core-api/internal/handlers"

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
	log.Println("✅ core-api: database ready")

	r := chi.NewRouter()
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Compress(5))
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{getEnv("CORS_ORIGIN", "http://localhost:3000"), "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"core-api"}`))
	})

	pathHandler := handlers.NewPathHandler(database)
	moduleHandler := handlers.NewModuleHandler(database)
	userHandler := handlers.NewUserHandler(database)
	waitlistHandler := handlers.NewWaitlistHandler(database)
	leaderboardHandler := handlers.NewLeaderboardHandler(database)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/waitlist", waitlistHandler.Join)
		r.Get("/leaderboard", leaderboardHandler.Get)

		r.Group(func(r chi.Router) {
			r.Use(middleware.JWTAuth)

			r.Get("/me", userHandler.Me)
			r.Patch("/me", userHandler.Update)
			r.Get("/me/activity", userHandler.Activity)

			r.Get("/paths", pathHandler.List)
			r.Post("/paths", pathHandler.Create)
			r.Get("/paths/{pathID}", pathHandler.Get)
			r.Delete("/paths/{pathID}", pathHandler.Delete)

			r.Get("/paths/{pathID}/modules", moduleHandler.List)
			r.Get("/modules/{moduleID}", moduleHandler.Get)
			r.Post("/modules/{moduleID}/start", moduleHandler.Start)
			r.Post("/modules/{moduleID}/complete", moduleHandler.Complete)
			r.Post("/modules/{moduleID}/feedback", moduleHandler.Feedback)
		})
	})

	port := getEnv("PORT", "8082")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("🚀 core-api running on port %s", port)
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
