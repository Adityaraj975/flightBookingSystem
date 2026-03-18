package handlers

import (
	"net/http"

	"flight-booking/internal/queue"

	"github.com/gin-gonic/gin"
)

type PaymentWebhookRequest struct {
	PaymentIntentID string `json:"paymentIntentId" binding:"required"`
	Status          string `json:"status" binding:"required"`
	PaidAt          string `json:"paidAt,omitempty"`
}

func HandlePaymentWebhook(producer *queue.Producer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req PaymentWebhookRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		event := queue.PaymentCallbackEvent{
			PaymentIntentID: req.PaymentIntentID,
			Status:          req.Status,
			PaidAt:          req.PaidAt,
		}
		if err := producer.PublishPaymentCallback(c.Request.Context(), event); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to enqueue"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"received": true})
	}
}
