package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/CN164/recommendation-service/internal/domain"
	"github.com/CN164/recommendation-service/internal/service"
	"github.com/gin-gonic/gin"
)

// Handler handles HTTP requests
type Handler struct {
	service *service.RecommendationService
}

// NewHandler creates a new handler
func NewHandler(service *service.RecommendationService) *Handler {
	return &Handler{
		service: service,
	}
}

// RegisterRoutes registers all HTTP routes
func (h *Handler) RegisterRoutes(router *gin.Engine) {
	router.GET("/users/:user_id/recommendations", h.GetUserRecommendations)
	router.GET("/recommendations/batch", h.GetBatchRecommendations)
	router.GET("/health", h.HealthCheck)
}

// GetUserRecommendations handles GET /users/{user_id}/recommendations
func (h *Handler) GetUserRecommendations(c *gin.Context) {
	// Parse and validate user_id
	userIDStr := c.Param("user_id")
	userID, err := strconv.ParseInt(userIDStr, 10, 64)
	if err != nil || userID <= 0 {
		c.JSON(http.StatusBadRequest, domain.ErrorResponse{
			Error:   "invalid_parameter",
			Message: "Invalid or missing user_id parameter",
		})
		return
	}

	// Parse and validate limit query parameter
	limitStr := c.DefaultQuery("limit", "10")
	limit, err := strconv.ParseInt(limitStr, 10, 32)
	if err != nil || limit < 1 || limit > 50 {
		c.JSON(http.StatusBadRequest, domain.ErrorResponse{
			Error:   "invalid_parameter",
			Message: "Invalid limit parameter (must be 1-50)",
		})
		return
	}

	// Get recommendations
	response, err := h.service.GetRecommendations(c.Request.Context(), userID, int32(limit))
	if err != nil {
		switch err.Error() {
		case "user_not_found":
			c.JSON(http.StatusNotFound, domain.ErrorResponse{
				Error:   "user_not_found",
				Message: "User with the specified ID does not exist",
			})
		case "model_unavailable":
			c.JSON(http.StatusServiceUnavailable, domain.ErrorResponse{
				Error:   "model_unavailable",
				Message: "Recommendation model is temporarily unavailable",
			})
		default:
			c.JSON(http.StatusInternalServerError, domain.ErrorResponse{
				Error:   "internal_error",
				Message: "An unexpected error occurred",
			})
		}
		return
	}

	c.JSON(http.StatusOK, response)
}

// GetBatchRecommendations handles GET /recommendations/batch
func (h *Handler) GetBatchRecommendations(c *gin.Context) {
	// Parse and validate page query parameter
	pageStr := c.DefaultQuery("page", "1")
	page, err := strconv.ParseInt(pageStr, 10, 32)
	if err != nil || page < 1 {
		c.JSON(http.StatusBadRequest, domain.ErrorResponse{
			Error:   "invalid_parameter",
			Message: "Invalid page parameter (must be >= 1)",
		})
		return
	}

	// Parse and validate limit query parameter
	limitStr := c.DefaultQuery("limit", "20")
	limitVal, err := strconv.ParseInt(limitStr, 10, 32)
	if err != nil || limitVal < 1 || limitVal > 100 {
		c.JSON(http.StatusBadRequest, domain.ErrorResponse{
			Error:   "invalid_parameter",
			Message: "Invalid limit parameter (must be 1-100)",
		})
		return
	}

	// Get batch recommendations
	response, err := h.service.BatchRecommendations(c.Request.Context(), int32(page), int32(limitVal))
	if err != nil {
		c.JSON(http.StatusInternalServerError, domain.ErrorResponse{
			Error:   "internal_error",
			Message: "An unexpected error occurred",
		})
		return
	}

	c.JSON(http.StatusOK, response)
}

// HealthCheck handles GET /health
func (h *Handler) HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"timestamp": time.Now().UTC(),
	})
}
