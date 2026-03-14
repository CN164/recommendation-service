package domain

import "time"

// User represents a user profile
type User struct {
	ID               int64
	Age              int
	Country          string
	SubscriptionType string // free | basic | premium
	CreatedAt        time.Time
}

// Content represents a content item (movie/show)
type Content struct {
	ID              int64
	Title           string
	Genre           string
	PopularityScore float64 // original popularity value from DB, never modified
	Score           float64 // computed recommendation score (set by Scorer)
	CreatedAt       time.Time
}

// WatchHistory represents a user's watch record
type WatchHistory struct {
	ID        int64
	UserID    int64
	ContentID int64
	Genre     string
	WatchedAt time.Time
}

// Recommendation represents a recommended content item with score
type Recommendation struct {
	ContentID       int64   `json:"content_id"`
	Title           string  `json:"title"`
	Genre           string  `json:"genre"`
	PopularityScore float64 `json:"popularity_score"`
	Score           float64 `json:"score"`
}

// UserRecommendationResponse for single user endpoint
type UserRecommendationResponse struct {
	UserID          int64                  `json:"user_id"`
	Recommendations []Recommendation       `json:"recommendations"`
	Metadata        RecommendationMetadata `json:"metadata"`
}

// RecommendationMetadata contains cache and generation info
type RecommendationMetadata struct {
	CacheHit    bool      `json:"cache_hit"`
	GeneratedAt time.Time `json:"generated_at"`
	TotalCount  int       `json:"total_count"`
}

// BatchResult represents a single user result in batch response
type BatchResult struct {
	UserID          int64            `json:"user_id"`
	Recommendations []Recommendation `json:"recommendations,omitempty"`
	Status          string           `json:"status"` // success | failed
	Error           string           `json:"error,omitempty"`
	Message         string           `json:"message,omitempty"`
}

// BatchRecommendationResponse for batch endpoint
type BatchRecommendationResponse struct {
	Page       int32         `json:"page"`
	Limit      int32         `json:"limit"`
	TotalUsers int64         `json:"total_users"`
	Results    []BatchResult `json:"results"`
	Summary    BatchSummary  `json:"summary"`
	Metadata   BatchMetadata `json:"metadata"`
}

// BatchSummary contains aggregated batch statistics
type BatchSummary struct {
	SuccessCount     int32 `json:"success_count"`
	FailedCount      int32 `json:"failed_count"`
	ProcessingTimeMs int64 `json:"processing_time_ms"`
}

// BatchMetadata contains generation info
type BatchMetadata struct {
	GeneratedAt time.Time `json:"generated_at"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}
