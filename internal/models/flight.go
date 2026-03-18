package models

import (
	"time"

	"flight-booking/internal/constants"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type Flight struct {
	ID            uuid.UUID              `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	CargoID       string                 `gorm:"type:varchar(64);not null" json:"cargoId"`
	Origin        string                 `gorm:"type:varchar(10);not null" json:"origin"`
	Destination   string                 `gorm:"type:varchar(10);not null" json:"destination"`
	DepartureTime time.Time              `gorm:"type:timestamptz;not null" json:"departureTime"`
	ArrivalTime   time.Time              `gorm:"type:timestamptz;not null" json:"arrivalTime"`
	Price         float64                `gorm:"type:decimal(10,2);not null" json:"price"`
	Status        constants.FlightStatus  `gorm:"type:flight_status;not null;default:'SCHEDULED'" json:"status"`
	ReservedSeats pq.StringArray         `gorm:"type:text[];not null;default:'{}'" json:"reservedSeats"`
	BookedSeats   pq.StringArray         `gorm:"type:text[];not null;default:'{}'" json:"bookedSeats"`
	TotalSeats    int                    `gorm:"type:int;not null;default:30" json:"totalSeats"`
	CreatedAt     time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
	UpdatedAt     time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
}

func (Flight) TableName() string { return "flights" }

func (f *Flight) AvailableSeats() []string {
	taken := make(map[string]bool)
	for _, s := range f.ReservedSeats {
		taken[s] = true
	}
	for _, s := range f.BookedSeats {
		taken[s] = true
	}
	available := []string{}
	for _, s := range constants.AllSeats {
		if !taken[s] {
			available = append(available, s)
		}
	}
	return available
}
