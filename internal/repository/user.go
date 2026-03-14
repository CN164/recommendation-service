package repository

import (
	"context"

	"github.com/CN164/recommendation-service/internal/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UserRepository handles user-related database queries
type UserRepository struct {
	db *pgxpool.Pool
}

// NewUserRepository creates a new user repository
func NewUserRepository(db *pgxpool.Pool) *UserRepository {
	return &UserRepository{db: db}
}

// GetUserByID retrieves a single user by ID
func (r *UserRepository) GetUserByID(ctx context.Context, userID int64) (*domain.User, error) {
	var user domain.User
	err := r.db.QueryRow(
		ctx,
		`SELECT id, age, country, subscription_type, created_at 
         FROM users WHERE id = $1`,
		userID,
	).Scan(&user.ID, &user.Age, &user.Country, &user.SubscriptionType, &user.CreatedAt)

	if err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUsersByIDs retrieves multiple users in bulk (void N+1)
func (r *UserRepository) GetUsersByIDs(ctx context.Context, userIDs []int64) (map[int64]*domain.User, error) {
	rows, err := r.db.Query(
		ctx,
		`SELECT id, age, country, subscription_type, created_at 
         FROM users WHERE id = ANY($1)`,
		userIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]*domain.User)
	for rows.Next() {
		var user domain.User
		if err := rows.Scan(&user.ID, &user.Age, &user.Country, &user.SubscriptionType, &user.CreatedAt); err != nil {
			return nil, err
		}
		result[user.ID] = &user
	}

	return result, rows.Err()
}

// GetUsersPaginated retrieves users paginated for batch processing
func (r *UserRepository) GetUsersPaginated(ctx context.Context, page, limit int32) ([]int64, int64, error) {
	// Get total count
	var totalCount int64
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&totalCount)
	if err != nil {
		return nil, 0, err
	}

	// Get paginated user IDs
	offset := (page - 1) * limit
	rows, err := r.db.Query(
		ctx,
		`SELECT id FROM users ORDER BY id LIMIT $1 OFFSET $2`,
		limit,
		offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var userIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		userIDs = append(userIDs, id)
	}

	return userIDs, totalCount, rows.Err()
}
