package main

import (
	"context"
	"log"
	"strings"
	"time"

	"flight-booking/internal/cache"
	"flight-booking/internal/config"
	"flight-booking/internal/constants"
	"flight-booking/internal/database"
	"flight-booking/internal/models"
	"flight-booking/internal/services"

	"gorm.io/gorm"
)

func main() {
	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatal("database:", err)
	}
	rdb := cache.NewRedisClient(cfg)
	cleanupSvc := services.NewCleanupService(db)

	ctx := context.Background()

	go runCleanup(cleanupSvc, cfg.CleanupIntervalSec)
	go runHotKeyRefresh(ctx, db, rdb, cfg)
	go runDepartureSweep(db, cfg.DepartureSweepIntervalSec)

	select {}
}

func runCleanup(svc *services.CleanupService, intervalSec int) {
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := svc.RunCleanup(100); err != nil {
			log.Printf("cleanup error: %v", err)
		}
	}
}

func runHotKeyRefresh(ctx context.Context, db *gorm.DB, rdb *cache.Client, cfg *config.Config) {
	ticker := time.NewTicker(time.Duration(cfg.HotKeyRefreshIntervalSec) * time.Second)
	defer ticker.Stop()
	ttl := time.Duration(cfg.HotKeyTTLSec) * time.Second
	for range ticker.C {
		// Sources
		keys, err := rdb.Keys(ctx, "metrics:search:from:*").Result()
		if err != nil {
			continue
		}
		for _, key := range keys {
			rest := strings.TrimPrefix(key, "metrics:search:from:")
			parts := strings.SplitN(rest, ":", 2)
			if len(parts) != 2 {
				continue
			}
			origin, date := parts[0], parts[1]
			count, err := rdb.Get(ctx, key).Int64()
			if err != nil || count < int64(cfg.SourceThreshold) {
				continue
			}
			_ = rdb.AddHotSource(ctx, date, origin)
			var flights []*models.Flight
			if err := db.Where("origin = ? AND status = ? AND DATE(departure_time AT TIME ZONE 'UTC') = ?", origin, constants.FlightScheduled, date).Find(&flights).Error; err != nil {
				continue
			}
			_ = rdb.SetHotFromFlights(ctx, origin, date, flights, ttl)
		}
		// Destinations
		keys, err = rdb.Keys(ctx, "metrics:search:to:*").Result()
		if err != nil {
			continue
		}
		for _, key := range keys {
			rest := strings.TrimPrefix(key, "metrics:search:to:")
			parts := strings.SplitN(rest, ":", 2)
			if len(parts) != 2 {
				continue
			}
			dest, date := parts[0], parts[1]
			count, err := rdb.Get(ctx, key).Int64()
			if err != nil || count < int64(cfg.DestinationThreshold) {
				continue
			}
			_ = rdb.AddHotDestination(ctx, date, dest)
			var flights []*models.Flight
			if err := db.Where("destination = ? AND status = ? AND DATE(departure_time AT TIME ZONE 'UTC') = ?", dest, constants.FlightScheduled, date).Find(&flights).Error; err != nil {
				continue
			}
			_ = rdb.SetHotToFlights(ctx, dest, date, flights, ttl)
		}
	}
}

func runDepartureSweep(db *gorm.DB, intervalSec int) {
	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if err := db.Exec("UPDATE flights SET status = $1::flight_status WHERE departure_time < NOW() AND status != $1::flight_status", constants.FlightDeparted).Error; err != nil {
			log.Printf("departure sweep error: %v", err)
		}
	}
}
