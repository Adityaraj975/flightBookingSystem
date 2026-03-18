package models

import (
	"time"

	"flight-booking/internal/constants"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/datatypes"
)

type BookingIntent struct {
	ID                    uuid.UUID                     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BookingIntentID       string                         `gorm:"type:varchar(64);uniqueIndex;not null" json:"bookingIntentId"`
	UserID                uuid.UUID                     `gorm:"type:uuid;not null" json:"userId"`
	FlightID              uuid.UUID                     `gorm:"type:uuid;not null" json:"flightId"`
	Seats                 pq.StringArray                `gorm:"type:text[];not null" json:"seats"`
	CustomerDetails       datatypes.JSON                `gorm:"type:jsonb;not null;default:'{}'" json:"customerDetails"`
	ActivePaymentIntentID string                        `gorm:"type:varchar(64)" json:"activePaymentIntentId"`
	Status                constants.BookingIntentStatus `gorm:"type:booking_intent_status;not null;default:'PENDING'" json:"status"`
	RetryCount            int                           `gorm:"type:int;not null;default:0" json:"retryCount"`
	Metadata              datatypes.JSON                `gorm:"type:jsonb;not null;default:'{}'" json:"metadata"`
	ExpiresAt             time.Time                     `gorm:"type:timestamptz;not null" json:"expiresAt"`
	CreatedAt             time.Time                     `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
	UpdatedAt             time.Time                     `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`

	Flight Flight `gorm:"foreignKey:FlightID" json:"flight,omitempty"`
}

func (BookingIntent) TableName() string { return "booking_intents" }
