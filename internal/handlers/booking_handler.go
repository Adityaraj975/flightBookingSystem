package handlers

import (
	"errors"
	"net/http"

	"flight-booking/internal/config"
	"flight-booking/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CreateBookingRequest struct {
	FlightID        string                 `json:"flightId" binding:"required"`
	Seats           []string               `json:"seats" binding:"required,min=1"`
	UserID          string                 `json:"userId" binding:"required"`
	CustomerDetails map[string]interface{} `json:"customerDetails" binding:"required"`
}

func CreateBooking(bookingSvc *services.BookingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req CreateBookingRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userUUID, err := uuid.Parse(req.UserID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid userId"})
			return
		}
		result, err := bookingSvc.CreateBooking(services.CreateBookingInput{
			FlightID:        req.FlightID,
			Seats:           req.Seats,
			UserID:          userUUID,
			CustomerDetails: req.CustomerDetails,
		})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, gin.H{
			"bookingIntentId":   result.BookingIntentID,
			"status":            result.Status,
			"seats":             result.Seats,
			"expiresAt":         result.ExpiresAt,
			"retryCount":        result.RetryCount,
			"maxRetries":        result.MaxRetries,
			"retriesRemaining":  result.MaxRetries - result.RetryCount,
		})
	}
}

func GetBooking(bookingSvc *services.BookingService, cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
			return
		}
		bi, err := bookingSvc.GetBooking(id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "booking not found"})
			return
		}
		retriesRemaining := cfg.MaxRetries - bi.RetryCount
		if retriesRemaining < 0 {
			retriesRemaining = 0
		}
		c.JSON(http.StatusOK, gin.H{
			"bookingIntentId":       bi.BookingIntentID,
			"flightId":              bi.FlightID.String(),
			"seats":                 bi.Seats,
			"status":                string(bi.Status),
			"activePaymentIntentId": bi.ActivePaymentIntentID,
			"retryCount":            bi.RetryCount,
			"maxRetries":            cfg.MaxRetries,
			"retriesRemaining":      retriesRemaining,
			"expiresAt":             bi.ExpiresAt,
			"createdAt":             bi.CreatedAt,
		})
	}
}

func RetryPayment(bookingSvc *services.BookingService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
			return
		}
		result, err := bookingSvc.RetryPayment(id)
		if err != nil {
			switch {
			case errors.Is(err, services.ErrMaxRetriesExhausted):
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			case errors.Is(err, services.ErrBookingTerminal), errors.Is(err, services.ErrBookingNotRetryable):
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			default:
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"bookingIntentId":    result.BookingIntentID,
			"newPaymentIntentId": result.NewPaymentIntentID,
			"status":             result.Status,
			"retryCount":         result.RetryCount,
			"retriesRemaining":   result.RetriesRemaining,
			"expiresAt":          result.ExpiresAt,
		})
	}
}
