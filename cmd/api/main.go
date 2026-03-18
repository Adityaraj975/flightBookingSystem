package main

import (
	"log"

	"flight-booking/internal/cache"
	"flight-booking/internal/config"
	"flight-booking/internal/database"
	"flight-booking/internal/handlers"
	"flight-booking/internal/queue"
	"flight-booking/internal/seed"
	"flight-booking/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func main() {
	serverInstanceID := uuid.New().String()

	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatal("database:", err)
	}
	seed.SeedIfEmpty(db)
	rdb := cache.NewRedisClient(cfg)
	producer := queue.NewProducer(cfg)

	flightSvc := services.NewFlightService(db, rdb, cfg)
	bookingSvc := services.NewBookingService(db, cfg)

	r := gin.Default()

	api := r.Group("/api")
	{
		api.GET("/instance", func(c *gin.Context) { c.JSON(200, gin.H{"instanceId": serverInstanceID}) })
		api.GET("/flights", handlers.ListFlights(flightSvc))
		api.GET("/flights/:id", handlers.GetFlight(flightSvc))
		api.POST("/bookings", handlers.CreateBooking(bookingSvc))
		api.GET("/bookings/:id", handlers.GetBooking(bookingSvc, cfg))
		api.POST("/bookings/:id/retry", handlers.RetryPayment(bookingSvc))
	}

	r.POST("/webhooks/payment", handlers.HandlePaymentWebhook(producer))

	r.StaticFile("/", "./web/index.html")
	r.Static("/web", "./web")

	log.Printf("API server listening on :%s", cfg.APIPort)
	r.Run(":" + cfg.APIPort)
}
