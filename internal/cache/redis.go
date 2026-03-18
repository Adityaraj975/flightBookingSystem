package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"flight-booking/internal/config"
	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/redis/go-redis/v9"
)

type Client struct {
	*redis.Client
}

func NewRedisClient(cfg *config.Config) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisAddr,
	})
	return &Client{Client: rdb}
}

func (c *Client) GetRouteFlights(ctx context.Context, origin, destination, date string) ([]*models.Flight, error) {
	key := fmt.Sprintf("flights:route:%s:%s:%s", origin, destination, date)
	data, err := c.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var flights []*models.Flight
	if err := json.Unmarshal(data, &flights); err != nil {
		return nil, err
	}
	return flights, nil
}

func (c *Client) SetRouteFlights(ctx context.Context, origin, destination, date string, flights []*models.Flight, ttl time.Duration) error {
	key := fmt.Sprintf("flights:route:%s:%s:%s", origin, destination, date)
	data, err := json.Marshal(flights)
	if err != nil {
		return err
	}
	return c.Set(ctx, key, data, ttl).Err()
}

func (c *Client) GetHotFromFlights(ctx context.Context, origin, date string) ([]*models.Flight, error) {
	key := fmt.Sprintf("flights:hot:from:%s:%s", origin, date)
	data, err := c.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var flights []*models.Flight
	if err := json.Unmarshal(data, &flights); err != nil {
		return nil, err
	}
	return flights, nil
}

func (c *Client) SetHotFromFlights(ctx context.Context, origin, date string, flights []*models.Flight, ttl time.Duration) error {
	key := fmt.Sprintf("flights:hot:from:%s:%s", origin, date)
	data, err := json.Marshal(flights)
	if err != nil {
		return err
	}
	return c.Set(ctx, key, data, ttl).Err()
}

func (c *Client) GetHotToFlights(ctx context.Context, destination, date string) ([]*models.Flight, error) {
	key := fmt.Sprintf("flights:hot:to:%s:%s", destination, date)
	data, err := c.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var flights []*models.Flight
	if err := json.Unmarshal(data, &flights); err != nil {
		return nil, err
	}
	return flights, nil
}

func (c *Client) SetHotToFlights(ctx context.Context, destination, date string, flights []*models.Flight, ttl time.Duration) error {
	key := fmt.Sprintf("flights:hot:to:%s:%s", destination, date)
	data, err := json.Marshal(flights)
	if err != nil {
		return err
	}
	return c.Set(ctx, key, data, ttl).Err()
}

func (c *Client) IsHotSource(ctx context.Context, date, origin string) (bool, error) {
	return c.SIsMember(ctx, "hot:sources:"+date, origin).Result()
}

func (c *Client) IsHotDestination(ctx context.Context, date, dest string) (bool, error) {
	return c.SIsMember(ctx, "hot:destinations:"+date, dest).Result()
}

func (c *Client) AddHotSource(ctx context.Context, date, origin string) error {
	return c.SAdd(ctx, "hot:sources:"+date, origin).Err()
}

func (c *Client) AddHotDestination(ctx context.Context, date, dest string) error {
	return c.SAdd(ctx, "hot:destinations:"+date, dest).Err()
}

func (c *Client) IncrSearchFrom(ctx context.Context, origin, date string) error {
	key := fmt.Sprintf("metrics:search:from:%s:%s", origin, date)
	pipe := c.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Client) IncrSearchTo(ctx context.Context, destination, date string) error {
	key := fmt.Sprintf("metrics:search:to:%s:%s", destination, date)
	pipe := c.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Client) IncrSearchRoute(ctx context.Context, origin, destination, date string) error {
	key := fmt.Sprintf("metrics:search:route:%s:%s:%s", origin, destination, date)
	pipe := c.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 24*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (c *Client) GetSearchFromCount(ctx context.Context, origin, date string) (int64, error) {
	key := fmt.Sprintf("metrics:search:from:%s:%s", origin, date)
	return c.Get(ctx, key).Int64()
}

func (c *Client) GetSearchToCount(ctx context.Context, dest, date string) (int64, error) {
	key := fmt.Sprintf("metrics:search:to:%s:%s", dest, date)
	return c.Get(ctx, key).Int64()
}

func (c *Client) KeyExists(ctx context.Context, key string) (bool, error) {
	n, err := c.Client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (c *Client) UpdateFlightInCachedList(ctx context.Context, key string, flightID string, reservedSeats, bookedSeats []string, status string) error {
	data, err := c.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return err
	}
	var flights []*models.Flight
	if err := json.Unmarshal(data, &flights); err != nil {
		return err
	}
	found := false
	for i, f := range flights {
		if f.ID.String() == flightID {
			flights[i].ReservedSeats = reservedSeats
			flights[i].BookedSeats = bookedSeats
			flights[i].Status = constants.FlightStatus(status)
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	newData, err := json.Marshal(flights)
	if err != nil {
		return fmt.Errorf("marshal cached flights: %w", err)
	}
	remainingTTL, err := c.TTL(ctx, key).Result()
	if err != nil || remainingTTL <= 0 {
		remainingTTL = 5 * time.Minute
	}
	return c.Set(ctx, key, newData, remainingTTL).Err()
}
