package cdc

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"flight-booking/internal/cache"

	"github.com/jackc/pgx/v5"
)

type CDCPayload struct {
	Op            string   `json:"op"`
	FlightID      string   `json:"flight_id"`
	Origin        string   `json:"origin"`
	Destination   string   `json:"destination"`
	DepartureDate string   `json:"departure_date"`
	Status        string   `json:"status"`
	ReservedSeats []string `json:"reserved_seats"`
	BookedSeats   []string `json:"booked_seats"`
}

func StartCDCListener(ctx context.Context, connString string, rdb *cache.Client) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}
		err := listenOnce(ctx, connString, rdb)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		log.Printf("cdc connection lost: %v, reconnecting in %v", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func listenOnce(ctx context.Context, connString string, rdb *cache.Client) error {
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "LISTEN flight_changes"); err != nil {
		return err
	}
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var payload CDCPayload
		if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
			log.Printf("cdc unmarshal error: %v", err)
			continue
		}
		routeKey := "flights:route:" + payload.Origin + ":" + payload.Destination + ":" + payload.DepartureDate
		exists, _ := rdb.KeyExists(ctx, routeKey)
		if exists {
			if err := rdb.UpdateFlightInCachedList(ctx, routeKey, payload.FlightID, payload.ReservedSeats, payload.BookedSeats, payload.Status); err != nil {
				log.Printf("cdc cache update error (route): %v", err)
			}
		}
		hotSource, _ := rdb.IsHotSource(ctx, payload.DepartureDate, payload.Origin)
		if hotSource {
			hotFromKey := "flights:hot:from:" + payload.Origin + ":" + payload.DepartureDate
			if err := rdb.UpdateFlightInCachedList(ctx, hotFromKey, payload.FlightID, payload.ReservedSeats, payload.BookedSeats, payload.Status); err != nil {
				log.Printf("cdc cache update error (hot-from): %v", err)
			}
		}
		hotDest, _ := rdb.IsHotDestination(ctx, payload.DepartureDate, payload.Destination)
		if hotDest {
			hotToKey := "flights:hot:to:" + payload.Destination + ":" + payload.DepartureDate
			if err := rdb.UpdateFlightInCachedList(ctx, hotToKey, payload.FlightID, payload.ReservedSeats, payload.BookedSeats, payload.Status); err != nil {
				log.Printf("cdc cache update error (hot-to): %v", err)
			}
		}
	}
}
