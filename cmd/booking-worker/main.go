package main

import (
	"context"
	"log"
	"os"
	"os/signal"

	"flight-booking/internal/config"
	"flight-booking/internal/database"
	"flight-booking/internal/queue"
	"flight-booking/internal/services"
)

func main() {
	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatal("database:", err)
	}
	consumer := queue.NewConsumer(cfg)
	callbackSvc := services.NewCallbackService(db)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Println("booking-worker: consuming payment.callbacks")
	consumer.Start(ctx, func(event queue.PaymentCallbackEvent) error {
		return callbackSvc.ProcessPaymentCallback(event.PaymentIntentID, event.Status)
	})
	log.Println("booking-worker: stopped")
}
