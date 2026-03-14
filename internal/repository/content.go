package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourname/recommendation-service-v2/internal/domain"
)

// ContentRepository handles content-related database queries
type ContentRepository struct {
	db *pgxpool.Pool
}

// NewContentRepository creates a new content repository
func NewContentRepository(db *pgxpool.Pool) *ContentRepository {
	return &ContentRepository{db: db}
}

// GetWatchHistory retrieves watch history for a single user
func (r *ContentRepository) GetWatchHistory(ctx context.Context, userID int64) ([]domain.WatchHistory, error) {
	rows, err := r.db.Query(
		ctx,
		`SELECT uwh.id, uwh.user_id, uwh.content_id, c.genre, uwh.watched_at
         FROM user_watch_history uwh
         JOIN content c ON uwh.content_id = c.id
         WHERE uwh.user_id = $1
         ORDER BY uwh.watched_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []domain.WatchHistory
	for rows.Next() {
		var wh domain.WatchHistory
		if err := rows.Scan(&wh.ID, &wh.UserID, &wh.ContentID, &wh.Genre, &wh.WatchedAt); err != nil {
			return nil, err
		}
		history = append(history, wh)
	}

	return history, rows.Err()
}

// GetWatchHistoryBulk retrieves watch history for multiple users (avoid N+1)
func (r *ContentRepository) GetWatchHistoryBulk(ctx context.Context, userIDs []int64) (map[int64][]domain.WatchHistory, error) {
	rows, err := r.db.Query(
		ctx,
		`SELECT uwh.user_id, uwh.id, c.genre, uwh.content_id, uwh.watched_at
         FROM user_watch_history uwh
         JOIN content c ON uwh.content_id = c.id
         WHERE uwh.user_id = ANY($1)
         ORDER BY uwh.user_id, uwh.watched_at DESC`,
		userIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][]domain.WatchHistory)
	for rows.Next() {
		var wh domain.WatchHistory
		if err := rows.Scan(&wh.UserID, &wh.ID, &wh.Genre, &wh.ContentID, &wh.WatchedAt); err != nil {
			return nil, err
		}
		result[wh.UserID] = append(result[wh.UserID], wh)
	}

	return result, rows.Err()
}

// GetAllContent retrieves top-N content ordered by popularity without user-specific filtering.
// Used by batch processing: a shared candidate pool is fetched once and each worker
// filters out its own watched content in-memory to avoid N+1 queries.
func (r *ContentRepository) GetAllContent(ctx context.Context, limit int32) ([]domain.Content, error) {
	rows, err := r.db.Query(
		ctx,
		`SELECT id, title, genre, popularity_score, created_at
         FROM content
         ORDER BY popularity_score DESC
         LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var content []domain.Content
	for rows.Next() {
		var c domain.Content
		if err := rows.Scan(&c.ID, &c.Title, &c.Genre, &c.PopularityScore, &c.CreatedAt); err != nil {
			return nil, err
		}
		content = append(content, c)
	}

	return content, rows.Err()
}

// GetCandidateContent retrieves content candidates for recommendation
// Excludes already-watched content and applies optional filters
func (r *ContentRepository) GetCandidateContent(ctx context.Context, userID int64, limit int32) ([]domain.Content, error) {
	rows, err := r.db.Query(
		ctx,
		`SELECT c.id, c.title, c.genre, c.popularity_score, c.created_at
         FROM content c
         WHERE c.id NOT IN (
             SELECT content_id FROM user_watch_history WHERE user_id = $1
         )
         ORDER BY c.popularity_score DESC
         LIMIT $2`,
		userID,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []domain.Content
	for rows.Next() {
		var c domain.Content
		if err := rows.Scan(&c.ID, &c.Title, &c.Genre, &c.PopularityScore, &c.CreatedAt); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}

	return candidates, rows.Err()
}
