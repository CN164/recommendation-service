// seed.go — standalone script to (re)seed the database.
// Usage: go run ./scripts/seed.go
// Requires DATABASE_URL env var (or defaults to local dev URL).
package main

import (
	"context"
	"log"
	"os"

	"github.com/CN164/recommendation-service/internal/seeder"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgresql://user:password@localhost:5432/recommendations?sslmode=disable"
	}

	ctx := context.Background()

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		log.Fatalf("Database not reachable: %v", err)
	}

	if err := seeder.Run(ctx, db); err != nil {
		log.Fatalf("Seed failed: %v", err)
	}
}
