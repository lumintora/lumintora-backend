package db

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"lumintora/migrations"

	_ "github.com/lib/pq"
)

func Connect() (*sql.DB, error) {
	// DATABASE_URL takes priority (Neon / Render / Heroku style).
	// Falls back to individual DB_* vars for local docker-compose.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			getEnv("DB_HOST", "localhost"),
			getEnv("DB_PORT", "5432"),
			getEnv("DB_USER", "lumintora"),
			getEnv("DB_PASSWORD", "lumintora_secret"),
			getEnv("DB_NAME", "lumintora"),
		)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	for i := 0; i < 10; i++ {
		if err := db.Ping(); err == nil {
			return db, nil
		}
		time.Sleep(2 * time.Second)
	}

	return nil, fmt.Errorf("could not connect to database after retries")
}

// Migrate applies all embedded SQL migrations in lexical filename order.
// Every migration is idempotent (IF NOT EXISTS / CREATE OR REPLACE), so running
// this on every startup is safe even when the schema already exists.
func Migrate(database *sql.DB) error {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := database.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
