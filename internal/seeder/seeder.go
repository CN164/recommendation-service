// Package seeder provides deterministic database seeding for the recommendation service.
// Uses a fixed random seed (42) so results are reproducible across runs.
package seeder

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run truncates all tables and inserts fresh seed data.
// Idempotent: safe to call multiple times.
func Run(ctx context.Context, db *pgxpool.Pool) error {
	// TRUNCATE in FK-safe order, restart sequences
	_, err := db.Exec(ctx, "TRUNCATE user_watch_history, content, users RESTART IDENTITY CASCADE")
	if err != nil {
		return fmt.Errorf("failed to truncate tables: %w", err)
	}
	log.Println("✓ Tables truncated")

	// Fixed seed for determinism (spec: seed = 42)
	rng := rand.New(rand.NewSource(42))

	genres := []string{"action", "drama", "comedy", "thriller", "documentary"}
	contentIDs, err := seedContent(ctx, db, rng, genres, 50)
	if err != nil {
		return fmt.Errorf("failed to seed content: %w", err)
	}
	log.Printf("✓ Seeded %d content items\n", len(contentIDs))

	countries := []string{"US", "GB", "CA", "AU", "DE", "TH", "JP"}
	subscriptions := []string{"free", "basic", "premium"}
	weights := []float64{0.5, 0.3, 0.2}
	userIDs, err := seedUsers(ctx, db, rng, countries, subscriptions, weights, 20)
	if err != nil {
		return fmt.Errorf("failed to seed users: %w", err)
	}
	log.Printf("✓ Seeded %d users\n", len(userIDs))

	watchCount, err := seedWatchHistory(ctx, db, rng, userIDs, contentIDs, 200)
	if err != nil {
		return fmt.Errorf("failed to seed watch history: %w", err)
	}
	log.Printf("✓ Seeded %d watch history records\n", watchCount)

	log.Printf("✅ Seed complete: %d users, %d content, %d watch history records\n",
		len(userIDs), len(contentIDs), watchCount)
	return nil
}

func seedContent(ctx context.Context, db *pgxpool.Pool, rng *rand.Rand, genres []string, count int) ([]int64, error) {
	type contentRow struct {
		id    int64
		score float64
	}

	rows := make([]contentRow, 0, count)
	for i := 0; i < count; i++ {
		genre := genres[rng.Intn(len(genres))]
		// Power-law popularity: x^2 skews toward lower values (long-tail distribution)
		popularity := math.Pow(rng.Float64(), 2.0)
		title := fmt.Sprintf("Content %d", i+1)
		createdAt := time.Now().Add(-time.Duration(rng.Intn(365)) * 24 * time.Hour)

		var id int64
		err := db.QueryRow(
			ctx,
			`INSERT INTO content (title, genre, popularity_score, created_at)
             VALUES ($1, $2, $3, $4) RETURNING id`,
			title, genre, popularity, createdAt,
		).Scan(&id)
		if err != nil {
			return nil, err
		}
		rows = append(rows, contentRow{id: id, score: popularity})
	}

	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.id
	}
	return ids, nil
}

func seedUsers(ctx context.Context, db *pgxpool.Pool, rng *rand.Rand,
	countries, subscriptions []string, weights []float64, count int) ([]int64, error) {

	ids := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		age := 18 + rng.Intn(50) // 18–67
		country := countries[rng.Intn(len(countries))]
		subscription := weightedChoice(rng, subscriptions, weights)
		createdAt := time.Now().Add(-time.Duration(rng.Intn(365)) * 24 * time.Hour)

		var id int64
		err := db.QueryRow(
			ctx,
			`INSERT INTO users (age, country, subscription_type, created_at)
             VALUES ($1, $2, $3, $4) RETURNING id`,
			age, country, subscription, createdAt,
		).Scan(&id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func seedWatchHistory(ctx context.Context, db *pgxpool.Pool, rng *rand.Rand,
	userIDs, contentIDs []int64, targetCount int) (int, error) {

	// Build popularity-weighted content pool using power-law scores
	type contentEntry struct {
		id    int64
		score float64
	}
	pool := make([]contentEntry, len(contentIDs))
	for i, cid := range contentIDs {
		pool[i] = contentEntry{id: cid, score: math.Pow(rng.Float64(), 2.0)}
	}

	totalWeight := 0.0
	for _, c := range pool {
		totalWeight += c.score
	}

	count := 0
	for i := 0; i < targetCount; i++ {
		userID := userIDs[rng.Intn(len(userIDs))]

		// Popularity-biased content selection
		r := rng.Float64() * totalWeight
		cumulative := 0.0
		var selectedContent int64
		for _, c := range pool {
			cumulative += c.score
			if r <= cumulative {
				selectedContent = c.id
				break
			}
		}
		if selectedContent == 0 {
			selectedContent = pool[len(pool)-1].id
		}

		watchedAt := time.Now().Add(-time.Duration(rng.Intn(365)) * 24 * time.Hour)
		_, err := db.Exec(
			ctx,
			`INSERT INTO user_watch_history (user_id, content_id, watched_at)
             VALUES ($1, $2, $3)
             ON CONFLICT DO NOTHING`,
			userID, selectedContent, watchedAt,
		)
		if err != nil {
			continue
		}
		count++
	}
	return count, nil
}

// weightedChoice selects an item from choices based on cumulative weights.
func weightedChoice(rng *rand.Rand, choices []string, weights []float64) string {
	r := rng.Float64()
	cumulative := 0.0
	for i, w := range weights {
		cumulative += w
		if r < cumulative {
			return choices[i]
		}
	}
	return choices[len(choices)-1]
}
