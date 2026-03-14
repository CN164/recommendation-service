package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/CN164/recommendation-service/internal/domain"
	"github.com/CN164/recommendation-service/internal/model"
	"github.com/jackc/pgx/v5"
)

// Sentinel errors returned to callers.
var (
	ErrUserNotFound     = errors.New("user not found")
	ErrModelUnavailable = errors.New("recommendation model unavailable")
)

// Dependency interfaces — defined in the consumer package so implementations
// can live in any package without creating import cycles.
type userRepo interface {
	GetUserByID(ctx context.Context, userID int64) (*domain.User, error)
	GetUsersByIDs(ctx context.Context, userIDs []int64) (map[int64]*domain.User, error)
	GetUsersPaginated(ctx context.Context, page, limit int32) ([]int64, int64, error)
}

type contentRepo interface {
	GetWatchHistory(ctx context.Context, userID int64) ([]domain.WatchHistory, error)
	GetWatchHistoryBulk(ctx context.Context, userIDs []int64) (map[int64][]domain.WatchHistory, error)
	GetCandidateContent(ctx context.Context, userID int64, limit int32) ([]domain.Content, error)
	GetAllContent(ctx context.Context, limit int32) ([]domain.Content, error)
}

type recommendationCache interface {
	Get(ctx context.Context, userID int64, limit int32) ([]domain.Recommendation, error)
	Set(ctx context.Context, userID int64, limit int32, recommendations []domain.Recommendation) error
}

type scorer interface {
	Score(candidates []domain.Content, watchHistory []domain.WatchHistory) ([]domain.Content, error)
}

const (
	workerPoolSize      = 10
	batchDefaultLimit   = 10
	batchPerUserTimeout = 3 * time.Second
	dbTimeout           = 5 * time.Second
	redisTimeout        = 1 * time.Second
)

// RecommendationService handles business logic for recommendations
type RecommendationService struct {
	userRepo    userRepo
	contentRepo contentRepo
	cache       recommendationCache
	scorer      scorer
}

// NewRecommendationService creates a new recommendation service
func NewRecommendationService(
	userRepo userRepo,
	contentRepo contentRepo,
	cache recommendationCache,
	scorer scorer,
) *RecommendationService {
	return &RecommendationService{
		userRepo:    userRepo,
		contentRepo: contentRepo,
		cache:       cache,
		scorer:      scorer,
	}
}

// GetRecommendations generates recommendations for a single user.
// Each DB/cache call gets its own derived context from the base request context
// so that cancelling one does not cancel subsequent calls.
func (s *RecommendationService) GetRecommendations(
	ctx context.Context,
	userID int64,
	limit int32,
) (*domain.UserRecommendationResponse, error) {
	// Save the original request context — every timeout derives from here, not
	// from a previously-cancelled child context.
	baseCtx := ctx

	// --- Cache check ---
	cacheCtx, cacheCancel := context.WithTimeout(baseCtx, redisTimeout)
	defer cacheCancel()
	cached, _ := s.cache.Get(cacheCtx, userID, limit)

	if len(cached) > 0 {
		return &domain.UserRecommendationResponse{
			UserID:          userID,
			Recommendations: cached,
			Metadata: domain.RecommendationMetadata{
				CacheHit:    true,
				GeneratedAt: time.Now(),
				TotalCount:  len(cached),
			},
		}, nil
	}

	// --- Cache miss: generate fresh recommendations ---

	// 1. Validate user exists
	userCtx, userCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer userCancel()
	user, err := s.userRepo.GetUserByID(userCtx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 2. Get watch history (for genre preference weights)
	histCtx, histCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer histCancel()
	watchHistory, err := s.contentRepo.GetWatchHistory(histCtx, userID)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 3. Get candidate content (excludes watched, ordered by popularity)
	candCtx, candCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer candCancel()
	candidates, err := s.contentRepo.GetCandidateContent(candCtx, userID, 100)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 4. Score candidates using model (mutates candidates[i].Score)
	scoredCandidates, err := s.scorer.Score(candidates, watchHistory)
	if err != nil {
		if errors.Is(err, model.ErrModelUnavailable) {
			return nil, ErrModelUnavailable
		}
		return nil, fmt.Errorf("model_error: %w", err)
	}

	// 5. Take top N
	topN := int(limit)
	if len(scoredCandidates) < topN {
		topN = len(scoredCandidates)
	}

	recommendations := make([]domain.Recommendation, topN)
	for i := 0; i < topN; i++ {
		c := scoredCandidates[i]
		recommendations[i] = domain.Recommendation{
			ContentID:       c.ID,
			Title:           c.Title,
			Genre:           c.Genre,
			PopularityScore: c.PopularityScore, // original value from DB
			Score:           c.Score,           // computed by scorer
		}
	}

	// 6. Cache the result (best-effort — a miss is not fatal)
	setCtx, setCancel := context.WithTimeout(baseCtx, redisTimeout)
	defer setCancel()
	if err := s.cache.Set(setCtx, userID, limit, recommendations); err != nil {
		log.Printf("cache set failed for user %d: %v", userID, err)
	}

	_ = user // available for subscription/geo filtering extensions

	return &domain.UserRecommendationResponse{
		UserID:          userID,
		Recommendations: recommendations,
		Metadata: domain.RecommendationMetadata{
			CacheHit:    false,
			GeneratedAt: time.Now(),
			TotalCount:  topN,
		},
	}, nil
}

// BatchRecommendations processes recommendations for a page of users concurrently.
// Avoids N+1 by bulk-prefetching user data and watch histories before spawning workers.
func (s *RecommendationService) BatchRecommendations(
	ctx context.Context,
	page, limit int32,
) (*domain.BatchRecommendationResponse, error) {
	baseCtx := ctx
	startTime := time.Now()

	// 1. Paginated user IDs
	pageCtx, pageCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer pageCancel()
	userIDs, totalCount, err := s.userRepo.GetUsersPaginated(pageCtx, page, limit)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	if len(userIDs) == 0 {
		return &domain.BatchRecommendationResponse{
			Page:       page,
			Limit:      limit,
			TotalUsers: totalCount,
			Results:    []domain.BatchResult{},
			Summary: domain.BatchSummary{
				SuccessCount:     0,
				FailedCount:      0,
				ProcessingTimeMs: time.Since(startTime).Milliseconds(),
			},
			Metadata: domain.BatchMetadata{GeneratedAt: time.Now()},
		}, nil
	}

	// 2. Bulk prefetch users (1 query for all users in this page)
	usersCtx, usersCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer usersCancel()
	users, err := s.userRepo.GetUsersByIDs(usersCtx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 3. Bulk prefetch watch histories (1 query for all users in this page)
	histCtx, histCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer histCancel()
	watchHistories, err := s.contentRepo.GetWatchHistoryBulk(histCtx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 4. Fetch shared content pool (1 query — workers filter watched content in-memory)
	contentCtx, contentCancel := context.WithTimeout(baseCtx, dbTimeout)
	defer contentCancel()
	allContent, err := s.contentRepo.GetAllContent(contentCtx, 500)
	if err != nil {
		return nil, fmt.Errorf("db_error: %w", err)
	}

	// 5. Fan-out to bounded worker pool
	jobs := make(chan int64, len(userIDs))
	results := make(chan *domain.BatchResult, len(userIDs))

	numWorkers := workerPoolSize
	if len(userIDs) < numWorkers {
		numWorkers = len(userIDs)
	}

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.batchWorker(baseCtx, jobs, results, users, watchHistories, allContent)
		}()
	}

	// Send all jobs then close the channel so workers can drain
	go func() {
		for _, uid := range userIDs {
			jobs <- uid
		}
		close(jobs)
	}()

	// Close results channel once all workers are done
	go func() {
		wg.Wait()
		close(results)
	}()

	// 6. Collect results
	var batchResults []domain.BatchResult
	var successCount, failedCount int32
	for result := range results {
		batchResults = append(batchResults, *result)
		if result.Status == "success" {
			successCount++
		} else {
			failedCount++
		}
	}

	return &domain.BatchRecommendationResponse{
		Page:       page,
		Limit:      limit,
		TotalUsers: totalCount,
		Results:    batchResults,
		Summary: domain.BatchSummary{
			SuccessCount:     successCount,
			FailedCount:      failedCount,
			ProcessingTimeMs: time.Since(startTime).Milliseconds(),
		},
		Metadata: domain.BatchMetadata{
			GeneratedAt: time.Now(),
		},
	}, nil
}

// batchWorker processes individual users from the jobs channel.
// Each worker:
//   - Checks cache first (fast path)
//   - On cache miss: filters allContent in-memory per user (no extra DB calls),
//     scores the filtered candidates, then caches and returns the result.
//
// Workers never share mutable state — each creates its own candidate copy.
func (s *RecommendationService) batchWorker(
	ctx context.Context,
	jobs <-chan int64,
	results chan<- *domain.BatchResult,
	users map[int64]*domain.User,
	watchHistories map[int64][]domain.WatchHistory,
	allContent []domain.Content,
) {
	for userID := range jobs {
		workerCtx, cancel := context.WithTimeout(ctx, batchPerUserTimeout)

		// --- Cache check ---
		recs, _ := s.cache.Get(workerCtx, userID, batchDefaultLimit)
		if len(recs) > 0 {
			results <- &domain.BatchResult{
				UserID:          userID,
				Recommendations: recs,
				Status:          "success",
			}
			cancel()
			continue
		}

		// --- Validate user ---
		_, ok := users[userID]
		if !ok {
			results <- &domain.BatchResult{
				UserID:  userID,
				Status:  "failed",
				Error:   "user_not_found",
				Message: fmt.Sprintf("User %d not found in prefetch map", userID),
			}
			cancel()
			continue
		}

		history := watchHistories[userID]

		// Build watched-content set for O(1) lookup
		watchedIDs := make(map[int64]struct{}, len(history))
		for _, wh := range history {
			watchedIDs[wh.ContentID] = struct{}{}
		}

		// Create a per-worker copy of candidates, excluding already-watched content.
		// This avoids both N+1 queries AND the shared-slice data race.
		userCandidates := make([]domain.Content, 0, len(allContent))
		for _, c := range allContent {
			if _, watched := watchedIDs[c.ID]; !watched {
				userCandidates = append(userCandidates, c) // value copy — safe to mutate
			}
		}

		// --- Score ---
		scoredCandidates, err := s.scorer.Score(userCandidates, history)
		if err != nil {
			results <- &domain.BatchResult{
				UserID:  userID,
				Status:  "failed",
				Error:   "model_inference_timeout",
				Message: "Recommendation generation exceeded timeout limit",
			}
			cancel()
			continue
		}

		// Take top batchDefaultLimit
		topN := batchDefaultLimit
		if len(scoredCandidates) < topN {
			topN = len(scoredCandidates)
		}

		recommendations := make([]domain.Recommendation, topN)
		for i := 0; i < topN; i++ {
			c := scoredCandidates[i]
			recommendations[i] = domain.Recommendation{
				ContentID:       c.ID,
				Title:           c.Title,
				Genre:           c.Genre,
				PopularityScore: c.PopularityScore,
				Score:           c.Score,
			}
		}

		// Cache result (best-effort: use a fresh short-lived context — workerCtx
		// may be near timeout and caching is non-critical)
		cacheCtx, cacheCancel := context.WithTimeout(context.Background(), time.Second)
		if err := s.cache.Set(cacheCtx, userID, batchDefaultLimit, recommendations); err != nil {
			log.Printf("cache set failed for batch user %d: %v", userID, err)
		}
		cacheCancel()

		results <- &domain.BatchResult{
			UserID:          userID,
			Recommendations: recommendations,
			Status:          "success",
		}

		cancel()
	}
}
