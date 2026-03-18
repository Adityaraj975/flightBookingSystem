package models

import (
	"time"

	"flight-booking/internal/constants"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type PaymentIntent struct {
	ID               uuid.UUID                   `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	PaymentIntentID  string                      `gorm:"type:varchar(64);uniqueIndex;not null" json:"paymentIntentId"`
	BookingIntentID  string                      `gorm:"type:varchar(64);not null;index" json:"bookingIntentId"`
	Amount           float64                     `gorm:"type:decimal(10,2);not null" json:"amount"`
	Status           constants.PaymentIntentStatus `gorm:"type:payment_intent_status;not null;default:'INITIATED'" json:"status"`
	Metadata         datatypes.JSON              `gorm:"type:jsonb;not null;default:'{}'" json:"metadata"`
	CreatedAt        time.Time                   `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
	UpdatedAt        time.Time                   `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
}

func (PaymentIntent) TableName() string { return "payment_intents" }
