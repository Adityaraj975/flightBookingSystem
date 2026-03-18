package services

import (
	"fmt"
	"log"

	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

type CallbackService struct {
	db *gorm.DB
}

func NewCallbackService(db *gorm.DB) *CallbackService {
	return &CallbackService{db: db}
}

func (s *CallbackService) ProcessPaymentCallback(paymentIntentID, callbackStatus string) error {
	var pi models.PaymentIntent
	if err := s.db.Where("payment_intent_id = ?", paymentIntentID).First(&pi).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			log.Printf("orphan payment callback: payment_intent_id=%s", paymentIntentID)
			return nil
		}
		return err
	}
	bookingIntentID := pi.BookingIntentID

	if callbackStatus != "SUCCESS" && callbackStatus != "FAILED" {
		log.Printf("unknown callback status %q for pi=%s, ignoring", callbackStatus, paymentIntentID)
		return fmt.Errorf("unknown callback status: %s", callbackStatus)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var bi models.BookingIntent
		res := tx.Raw("SELECT * FROM booking_intents WHERE booking_intent_id = ? FOR UPDATE", bookingIntentID).Scan(&bi)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			log.Printf("booking intent not found for pi=%s, bi=%s", paymentIntentID, bookingIntentID)
			return fmt.Errorf("booking intent not found: %s", bookingIntentID)
		}

		var flight models.Flight
		res = tx.Raw("SELECT * FROM flights WHERE id = ? FOR UPDATE", bi.FlightID).Scan(&flight)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("flight not found: %s", bi.FlightID)
		}

		if bi.Status.IsTerminal() {
			if callbackStatus == "SUCCESS" {
				if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentSuccess).Error; err != nil {
					return err
				}
				log.Printf("REFUND: payment_intent_id=%s (booking already terminal)", paymentIntentID)
			} else {
				if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentFailed).Error; err != nil {
					return err
				}
			}
			return nil
		}

		isActivePI := paymentIntentID == bi.ActivePaymentIntentID

		if isActivePI && callbackStatus == "SUCCESS" {
			if err := tx.Model(&models.BookingIntent{}).Where("booking_intent_id = ?", bookingIntentID).Updates(map[string]interface{}{
				"status": constants.BookingConfirmed,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentSuccess).Error; err != nil {
				return err
			}

			reserved := flight.ReservedSeats
			booked := flight.BookedSeats
			if reserved == nil {
				reserved = []string{}
			}
			if booked == nil {
				booked = []string{}
			}
			newReserved := removeSeats(reserved, bi.Seats)
			newBooked := make([]string, len(booked), len(booked)+len(bi.Seats))
			copy(newBooked, booked)
			newBooked = append(newBooked, bi.Seats...)
			if newReserved == nil {
				newReserved = []string{}
			}
			status := string(flight.Status)
			if len(newBooked) >= flight.TotalSeats {
				status = string(constants.FlightFullyBooked)
			}
			if err := tx.Exec(`
				UPDATE flights SET reserved_seats = $1, booked_seats = $2, status = $3::flight_status WHERE id = $4`,
				pq.Array(newReserved), pq.Array(newBooked), status, flight.ID).Error; err != nil {
				return err
			}

			if err := tx.Exec(`
				UPDATE payment_intents SET status = 'EXPIRED'
				WHERE booking_intent_id = ? AND status = 'INITIATED' AND payment_intent_id != ?`,
				bookingIntentID, paymentIntentID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM booking_intent_cleanup WHERE booking_intent_id = ?", bookingIntentID).Error; err != nil {
				return err
			}
			return nil
		}

		if isActivePI && callbackStatus == "FAILED" {
			if err := tx.Model(&models.BookingIntent{}).Where("booking_intent_id = ?", bookingIntentID).Updates(map[string]interface{}{
				"status": constants.BookingPaymentFailed,
			}).Error; err != nil {
				return err
			}
			if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentFailed).Error; err != nil {
				return err
			}
			return nil
		}

		if !isActivePI && callbackStatus == "SUCCESS" {
			if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentSuccess).Error; err != nil {
				return err
			}
			log.Printf("REFUND: payment_intent_id=%s (stale success)", paymentIntentID)
			return nil
		}

		if !isActivePI && callbackStatus == "FAILED" {
			if err := tx.Model(&models.PaymentIntent{}).Where("payment_intent_id = ?", paymentIntentID).Update("status", constants.PaymentFailed).Error; err != nil {
				return err
			}
			return nil
		}

		return nil
	})
}

func removeSeats(from, toRemove []string) []string {
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
