package services

import (
	"context"
	"time"

	"flight-booking/internal/cache"
	"flight-booking/internal/config"
	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type FlightService struct {
	db  *gorm.DB
	rdb *cache.Client
	cfg *config.Config
}

func NewFlightService(db *gorm.DB, rdb *cache.Client, cfg *config.Config) *FlightService {
	return &FlightService{db: db, rdb: rdb, cfg: cfg}
}

func (s *FlightService) ListFlights(ctx context.Context, origin, destination, date string, minPrice, maxPrice *float64) ([]*models.Flight, error) {
	go func() {
		_ = s.rdb.IncrSearchFrom(context.Background(), origin, date)
		_ = s.rdb.IncrSearchTo(context.Background(), destination, date)
		_ = s.rdb.IncrSearchRoute(context.Background(), origin, destination, date)
	}()

	hot, _ := s.rdb.IsHotSource(ctx, date, origin)
	if hot {
		flights, err := s.rdb.GetHotFromFlights(ctx, origin, date)
		if err == nil && len(flights) > 0 {
			return s.filterFlights(flights, destination, minPrice, maxPrice), nil
		}
	}

	flights, err := s.rdb.GetRouteFlights(ctx, origin, destination, date)
	if err == nil && len(flights) > 0 {
		return s.filterFlights(flights, "", minPrice, maxPrice), nil
	}

	var list []*models.Flight
	err = s.db.Where("origin = ? AND destination = ? AND status = ?",
		origin, destination, constants.FlightScheduled).
		Where("DATE(departure_time AT TIME ZONE 'UTC') = ?", date).
		Find(&list).Error
	if err != nil {
		return nil, err
	}

	ttl := s.routeCacheTTL(date)
	_ = s.rdb.SetRouteFlights(ctx, origin, destination, date, list, ttl)
	return s.filterFlights(list, "", minPrice, maxPrice), nil
}

func (s *FlightService) filterFlights(flights []*models.Flight, destFilter string, minPrice, maxPrice *float64) []*models.Flight {
	var out []*models.Flight
	for _, f := range flights {
		if destFilter != "" && f.Destination != destFilter {
			continue
		}
		if minPrice != nil && f.Price < *minPrice {
			continue
		}
		if maxPrice != nil && f.Price > *maxPrice {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (s *FlightService) routeCacheTTL(dateStr string) time.Duration {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return 24 * time.Hour
	}
	end := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, time.UTC)
	ttl := time.Until(end)
	if ttl < 0 {
		return time.Hour
	}
	return ttl
}

func (s *FlightService) GetFlight(ctx context.Context, id string) (*models.Flight, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	var f models.Flight
	if err := s.db.First(&f, "id = ?", uid).Error; err != nil {
		return nil, err
	}
	return &f, nil
}
