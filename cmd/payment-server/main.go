package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"flight-booking/internal/config"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg := config.Load()
	r := gin.Default()
	r.POST("/create-payment", handleCreatePayment(cfg))
	log.Printf("payment-server listening on :%d", cfg.PaymentServerPort)
	r.Run(":" + fmt.Sprint(cfg.PaymentServerPort))
}

func handleCreatePayment(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			PaymentIntentID string  `json:"paymentIntentId"`
			Amount          float64 `json:"amount"`
			Currency        string  `json:"currency"`
			CallbackURL     string  `json:"callbackUrl"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ACKNOWLEDGED", "paymentIntentId": req.PaymentIntentID})

		go func() {
			delaySec := cfg.PaymentMinDelaySec + rand.Intn(cfg.PaymentMaxDelaySec-cfg.PaymentMinDelaySec+1)
			if delaySec < 1 {
				delaySec = 1
			}
			time.Sleep(time.Duration(delaySec) * time.Second)

			if rand.Intn(100) < cfg.PaymentNoCallbackRate {
				return
			}

			status := "FAILED"
			if rand.Intn(100) < cfg.PaymentSuccessRate {
				status = "SUCCESS"
			}
			payload := map[string]interface{}{
				"paymentIntentId": req.PaymentIntentID,
				"status":          status,
				"paidAt":          time.Now().UTC().Format(time.RFC3339),
			}
			data, _ := json.Marshal(payload)
			resp, err := http.Post(req.CallbackURL, "application/json", bytes.NewReader(data))
			if err != nil {
				log.Printf("payment callback delivery failed: %v, retrying...", err)
				time.Sleep(5 * time.Second)
				if retryResp, retryErr := http.Post(req.CallbackURL, "application/json", bytes.NewReader(data)); retryErr == nil {
					retryResp.Body.Close()
				}
				return
			}
			resp.Body.Close()

			if rand.Intn(100) < cfg.PaymentDuplicateRate {
				time.Sleep(2 * time.Second)
				if dupResp, dupErr := http.Post(req.CallbackURL, "application/json", bytes.NewReader(data)); dupErr == nil {
					dupResp.Body.Close()
				}
			}
		}()
	}
}
