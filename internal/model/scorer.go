package model

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/CN164/recommendation-service/internal/domain"
)

// ErrModelUnavailable indicates the model is temporarily unavailable (503)
var ErrModelUnavailable = fmt.Errorf("model unavailable")

// Scorer is the recommendation model/scorer
type Scorer struct {
	rng *rand.Rand
	mu  sync.Mutex // protects rng — rand.Rand is not goroutine-safe
}

// NewScorer creates a new scorer with seeded randomness
func NewScorer() *Scorer {
	return &Scorer{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Score computes recommendation scores for candidates based on user watch history.
// Returns error if model simulation triggers failure (1.5% chance).
// Candidates slice is mutated in-place (Score field set); callers must pass a copy
// when calling concurrently to avoid data races.
func (s *Scorer) Score(candidates []domain.Content, watchHistory []domain.WatchHistory) ([]domain.Content, error) {
	// Grab all random values under a single lock to keep critical section short
	s.mu.Lock()
	sleepMs := 30 + s.rng.Intn(21)
	failureRoll := s.rng.Float64()
	noises := make([]float64, len(candidates))
	for i := range noises {
		noises[i] = (s.rng.Float64()*0.01 - 0.005) * 0.10
	}
	s.mu.Unlock()

	// Simulate model latency: 30-50ms (outside lock)
	time.Sleep(time.Duration(sleepMs) * time.Millisecond)

	// Simulate 1.5% failure rate
	if failureRoll < 0.015 {
		return nil, ErrModelUnavailable
	}

	// Build genre preference weights from watch history
	genreCounts := make(map[string]int)
	totalWatches := 0
	for _, item := range watchHistory {
		genreCounts[item.Genre]++
		totalWatches++
	}

	genrePrefs := make(map[string]float64)
	if totalWatches > 0 {
		for genre, count := range genreCounts {
			genrePrefs[genre] = float64(count) / float64(totalWatches)
		}
	}

	// Score each candidate — writes to .Score, never touches .PopularityScore
	now := time.Now()
	for i := range candidates {
		popularityComponent := candidates[i].PopularityScore * 0.40

		genreBoost := 0.10 // default for unseen genres
		if pref, ok := genrePrefs[candidates[i].Genre]; ok {
			genreBoost = pref
		}
		genreComponent := genreBoost * 0.35

		daysSince := now.Sub(candidates[i].CreatedAt).Hours() / 24
		recencyComponent := (1.0 / (1.0 + daysSince/365.0)) * 0.15

		candidates[i].Score = popularityComponent + genreComponent + recencyComponent + noises[i]
	}

	// Sort by Score descending using sort.Slice (O(n log n))
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates, nil
}
