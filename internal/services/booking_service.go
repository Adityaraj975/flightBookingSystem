package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"flight-booking/internal/config"
	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var (
	ErrMaxRetriesExhausted = errors.New("max retries exhausted")
	ErrBookingTerminal     = errors.New("booking is in a terminal state")
	ErrBookingNotRetryable = errors.New("cannot retry in current state")
)

var paymentHTTPClient = &http.Client{Timeout: 10 * time.Second}

type BookingService struct {
	db         *gorm.DB
	cfg        *config.Config
	paymentURL string
}

func NewBookingService(db *gorm.DB, cfg *config.Config) *BookingService {
	paymentURL := fmt.Sprintf("http://localhost:%d/create-payment", cfg.PaymentServerPort)
	return &BookingService{db: db, cfg: cfg, paymentURL: paymentURL}
}

var validSeats map[string]bool

func init() {
	validSeats = make(map[string]bool, len(constants.AllSeats))
	for _, s := range constants.AllSeats {
		validSeats[s] = true
	}
}

type CreateBookingInput struct {
	FlightID        string
	Seats           []string
	UserID          uuid.UUID
	CustomerDetails map[string]interface{}
}

type CreateBookingResult struct {
	BookingIntentID string
	Status          string
	Seats           []string
	ExpiresAt       time.Time
	RetryCount      int
	MaxRetries      int
}

func (s *BookingService) CreateBooking(in CreateBookingInput) (*CreateBookingResult, error) {
	bookingIntentID := "bi_" + uuid.New().String()
	paymentIntentID := "pi_" + uuid.New().String()
	expiresAt := time.Now().Add(time.Duration(s.cfg.InitialReservationMinutes) * time.Minute)

	flightUUID, err := uuid.Parse(in.FlightID)
	if err != nil {
		return nil, fmt.Errorf("invalid flight ID: %w", err)
	}
	for _, seat := range in.Seats {
		if !validSeats[seat] {
			return nil, fmt.Errorf("invalid seat name: %s", seat)
		}
	}

	var result *CreateBookingResult
	var payAmount float64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var flight models.Flight
		res := tx.Raw("SELECT * FROM flights WHERE id = ? FOR UPDATE", in.FlightID).Scan(&flight)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("flight not found")
		}
		if flight.Status != constants.FlightScheduled {
			return fmt.Errorf("flight not available")
		}
		taken := make(map[string]bool)
		for _, s := range flight.ReservedSeats {
			taken[s] = true
		}
		for _, s := range flight.BookedSeats {
			taken[s] = true
		}
		for _, seat := range in.Seats {
			if taken[seat] {
				return fmt.Errorf("seat %s unavailable", seat)
			}
		}

		if err := tx.Exec("UPDATE flights SET reserved_seats = array_cat(reserved_seats, $1) WHERE id = $2",
			pq.Array(in.Seats), in.FlightID).Error; err != nil {
			return err
		}

		payAmount = flight.Price * float64(len(in.Seats))
		cd, err := json.Marshal(in.CustomerDetails)
		if err != nil {
			return fmt.Errorf("marshal customer details: %w", err)
		}

		if err := tx.Exec(`
			INSERT INTO booking_intents (booking_intent_id, user_id, flight_id, seats, customer_details, active_payment_intent_id, status, retry_count, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'PENDING', 0, $7)`,
			bookingIntentID, in.UserID, flightUUID, pq.Array(in.Seats), datatypes.JSON(cd), paymentIntentID, expiresAt).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO payment_intents (payment_intent_id, booking_intent_id, amount, status)
			VALUES ($1, $2, $3, 'INITIATED')`,
			paymentIntentID, bookingIntentID, payAmount).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO booking_intent_cleanup (booking_intent_id, expiry) VALUES ($1, $2)`,
			bookingIntentID, expiresAt).Error; err != nil {
			return err
		}

		result = &CreateBookingResult{
			BookingIntentID: bookingIntentID,
			Status:          "PENDING",
			Seats:           in.Seats,
			ExpiresAt:       expiresAt,
			RetryCount:      0,
			MaxRetries:      s.cfg.MaxRetries,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	go s.callPaymentServer(paymentIntentID, payAmount)
	return result, nil
}

func (s *BookingService) callPaymentServer(paymentIntentID string, amount float64) {
	body := map[string]interface{}{
		"paymentIntentId": paymentIntentID,
		"amount":          amount,
		"currency":        "INR",
		"callbackUrl":     s.cfg.PaymentCallbackBaseURL,
	}
	data, err := json.Marshal(body)
	if err != nil {
		log.Printf("payment call marshal error pi=%s: %v", paymentIntentID, err)
		return
	}
	resp, err := paymentHTTPClient.Post(s.paymentURL, "application/json", bytes.NewReader(data))
	if err != nil {
		log.Printf("payment call failed pi=%s: %v", paymentIntentID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("payment server returned %d for pi=%s", resp.StatusCode, paymentIntentID)
	}
}

type RetryPaymentResult struct {
	BookingIntentID    string
	NewPaymentIntentID string
	Status             string
	RetryCount         int
	RetriesRemaining   int
	ExpiresAt          time.Time
}

func (s *BookingService) RetryPayment(bookingIntentID string) (*RetryPaymentResult, error) {
	newPaymentIntentID := "pi_" + uuid.New().String()
	newExpiresAt := time.Now().Add(time.Duration(s.cfg.RetryExtensionMinutes) * time.Minute)

	var amount float64
	var result *RetryPaymentResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var bi models.BookingIntent
		res := tx.Raw("SELECT * FROM booking_intents WHERE booking_intent_id = ? FOR UPDATE", bookingIntentID).Scan(&bi)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("booking not found")
		}
		if bi.Status.IsTerminal() {
			return fmt.Errorf("%w: booking is %s", ErrBookingTerminal, bi.Status)
		}
		if !bi.Status.IsRetryable() {
			return ErrBookingNotRetryable
		}
		if bi.RetryCount >= s.cfg.MaxRetries {
			return fmt.Errorf("%w (%d/%d)", ErrMaxRetriesExhausted, bi.RetryCount, s.cfg.MaxRetries)
		}

		var pi models.PaymentIntent
		if err := tx.Where("booking_intent_id = ?", bookingIntentID).Order("created_at").First(&pi).Error; err != nil {
			return err
		}
		amount = pi.Amount

		if err := tx.Exec(`
			UPDATE booking_intents SET active_payment_intent_id = $1, retry_count = retry_count + 1, status = 'PENDING', expires_at = $2
			WHERE booking_intent_id = $3`, newPaymentIntentID, newExpiresAt, bookingIntentID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO payment_intents (payment_intent_id, booking_intent_id, amount, status) VALUES ($1, $2, $3, 'INITIATED')`,
			newPaymentIntentID, bookingIntentID, amount).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE booking_intent_cleanup SET expiry = $1 WHERE booking_intent_id = $2`, newExpiresAt, bookingIntentID).Error; err != nil {
			return err
		}

		result = &RetryPaymentResult{
			BookingIntentID:    bookingIntentID,
			NewPaymentIntentID: newPaymentIntentID,
			Status:             "PENDING",
			RetryCount:         bi.RetryCount + 1,
			RetriesRemaining:   s.cfg.MaxRetries - (bi.RetryCount + 1),
			ExpiresAt:          newExpiresAt,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	go s.callPaymentServer(newPaymentIntentID, amount)
	return result, nil
}

func (s *BookingService) GetBooking(bookingIntentID string) (*models.BookingIntent, error) {
	var list []models.BookingIntent
	if err := s.db.Preload("Flight").Where("booking_intent_id = ?", bookingIntentID).Limit(1).Find(&list).Error; err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return &list[0], nil
}
