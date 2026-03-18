package models

import (
	"time"

	"github.com/google/uuid"
)

type BookingIntentCleanup struct {
	ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	BookingIntentID string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"bookingIntentId"`
	Expiry          time.Time `gorm:"type:timestamptz;not null;index" json:"expiry"`
}

func (BookingIntentCleanup) TableName() string { return "booking_intent_cleanup" }
