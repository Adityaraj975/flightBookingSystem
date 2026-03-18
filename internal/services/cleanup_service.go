package services

import (
	"fmt"
	"log"
	"time"

	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

type CleanupService struct {
	db *gorm.DB
}

func NewCleanupService(db *gorm.DB) *CleanupService {
	return &CleanupService{db: db}
}

func (s *CleanupService) RunCleanup(batchSize int) error {
	var cleanups []models.BookingIntentCleanup
	if err := s.db.Where("expiry <= ?", time.Now()).Order("expiry ASC").Limit(batchSize).Find(&cleanups).Error; err != nil {
		return err
	}

	for _, cleanup := range cleanups {
		if err := s.processCleanupRow(cleanup); err != nil {
			log.Printf("cleanup error for bi=%s: %v", cleanup.BookingIntentID, err)
			continue
		}
	}
	return nil
}

func (s *CleanupService) processCleanupRow(cleanup models.BookingIntentCleanup) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var bi models.BookingIntent
		res := tx.Raw("SELECT * FROM booking_intents WHERE booking_intent_id = ? FOR UPDATE", cleanup.BookingIntentID).Scan(&bi)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("booking intent not found: %s", cleanup.BookingIntentID)
		}

		if bi.Status.IsTerminal() {
			return tx.Exec("DELETE FROM booking_intent_cleanup WHERE id = ?", cleanup.ID).Error
		}

		if bi.ExpiresAt.After(time.Now()) {
			return tx.Exec("UPDATE booking_intent_cleanup SET expiry = ? WHERE id = ?", bi.ExpiresAt, cleanup.ID).Error
		}

		var flight models.Flight
		res = tx.Raw("SELECT * FROM flights WHERE id = ? FOR UPDATE", bi.FlightID).Scan(&flight)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("flight not found: %s", bi.FlightID)
		}

		if err := tx.Model(&models.BookingIntent{}).Where("booking_intent_id = ?", cleanup.BookingIntentID).Update("status", constants.BookingExpired).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE payment_intents SET status = 'EXPIRED'
			WHERE booking_intent_id = ? AND status = 'INITIATED'`, cleanup.BookingIntentID).Error; err != nil {
			return err
		}

		reserved := flight.ReservedSeats
		if reserved == nil {
			reserved = []string{}
		}
		newReserved := removeSeatsSlice(reserved, bi.Seats)
		if newReserved == nil {
			newReserved = []string{}
		}
		if err := tx.Exec("UPDATE flights SET reserved_seats = $1 WHERE id = $2", pq.Array(newReserved), flight.ID).Error; err != nil {
			return err
		}
		if flight.Status == constants.FlightFullyBooked && len(newReserved)+len(flight.BookedSeats) < flight.TotalSeats {
			if err := tx.Model(&models.Flight{}).Where("id = ?", flight.ID).Update("status", constants.FlightScheduled).Error; err != nil {
				return err
			}
		}

		return tx.Exec("DELETE FROM booking_intent_cleanup WHERE id = ?", cleanup.ID).Error
	})
}

func removeSeatsSlice(from, toRemove []string) []string {
	rm := make(map[string]bool)
	for _, s := range toRemove {
		rm[s] = true
	}
	var out []string
	for _, s := range from {
		if !rm[s] {
			out = append(out, s)
		}
	}
	return out
}
