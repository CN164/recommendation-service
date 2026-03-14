package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CN164/recommendation-service/internal/cache"
	"github.com/CN164/recommendation-service/internal/handler"
	"github.com/CN164/recommendation-service/internal/model"
	"github.com/CN164/recommendation-service/internal/repository"
	"github.com/CN164/recommendation-service/internal/seeder"
	"github.com/CN164/recommendation-service/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	// --- Environment variables ---
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgresql://user:password@localhost:5432/recommendations?sslmode=disable"
	}

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}

	serverPort := os.Getenv("SERVER_PORT")
	if serverPort == "" {
		serverPort = "8080"
	}

	// --- Database connection pool ---
	dbConfig, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Fatalf("Failed to parse DATABASE_URL: %v", err)
	}
	dbConfig.MaxConns = 20
	dbConfig.MinConns = 5
	dbConfig.MaxConnLifetime = time.Hour
	dbConfig.MaxConnIdleTime = 30 * time.Minute
	dbConfig.HealthCheckPeriod = time.Minute

	dbPool, err := pgxpool.NewWithConfig(context.Background(), dbConfig)
	if err != nil {
		log.Fatalf("Failed to create database pool: %v", err)
	}
	defer dbPool.Close()

	pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer pingCancel()
	if err := dbPool.Ping(pingCtx); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("✓ Database connection successful")

	// --- Run migrations (idempotent — safe on every startup) ---
	if err := runMigrations(dbURL); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
	log.Println("✓ Migrations applied")

	// --- Seed database only when empty ---
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer seedCancel()
	if err := seedIfEmpty(seedCtx, dbPool); err != nil {
		log.Printf("Warning: seed failed: %v", err)
	}

	// --- Redis cache ---
	cacheLayer, err := cache.NewCache(redisURL)
	if err != nil {
		log.Fatalf("Failed to initialise Redis: %v", err)
	}
	defer cacheLayer.Close()
	log.Println("✓ Redis connection successful")

	// --- Wire dependencies ---
	userRepo := repository.NewUserRepository(dbPool)
	contentRepo := repository.NewContentRepository(dbPool)
	scorer := model.NewScorer()
	recService := service.NewRecommendationService(userRepo, contentRepo, cacheLayer, scorer)

	// --- HTTP router ---
	// Use gin.New() instead of gin.Default() to control middleware order explicitly.
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(gin.Recovery())

	h := handler.NewHandler(recService)
	h.RegisterRoutes(router)

	// --- HTTP server with graceful shutdown ---
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%s", serverPort),
		Handler: router,
	}

	go func() {
		log.Printf("🚀 Server starting on :%s\n", serverPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v\n", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan
	log.Println("Shutting down server...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown error: %v", err)
	}
	log.Println("✓ Server stopped")
}

// runMigrations applies pending migrations using golang-migrate.
// All migration SQL files use IF NOT EXISTS so this is idempotent.
func runMigrations(dbURL string) error {
	// golang-migrate pgx/v5 driver uses the pgx5:// scheme
	migrateURL := strings.NewReplacer(
		"postgresql://", "pgx5://",
		"postgres://", "pgx5://",
	).Replace(dbURL)

	m, err := migrate.New("file://migrations", migrateURL)
	if err != nil {
		return fmt.Errorf("failed to initialise migrator: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("up migration failed: %w", err)
	}
	return nil
}

// seedIfEmpty seeds the database when the users table is empty.
// On subsequent restarts (tables already have data) the seed is skipped.
func seedIfEmpty(ctx context.Context, db *pgxpool.Pool) error {
	var count int64
	if err := db.QueryRow(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return fmt.Errorf("failed to count users: %w", err)
	}

	if count > 0 {
		log.Printf("✓ Database already seeded (%d users)\n", count)
		return nil
	}

	log.Println("Empty DB detected → running seed...")
	return seeder.Run(ctx, db)
}
