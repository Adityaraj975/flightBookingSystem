package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"

	"flight-booking/internal/cache"
	"flight-booking/internal/config"
	"flight-booking/internal/cdc"
)

func main() {
	cfg := config.Load()
	connString := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName, cfg.DBSSLMode)
	rdb := cache.NewRedisClient(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	log.Println("cdc-worker: listening for flight_changes")
	if err := cdc.StartCDCListener(ctx, connString, rdb); err != nil && ctx.Err() == nil {
		log.Fatal("cdc:", err)
	}
	log.Println("cdc-worker: stopped")
}
