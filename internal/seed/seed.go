package seed

import (
	"log"
	"math/rand"
	"time"

	"flight-booking/internal/constants"
	"flight-booking/internal/models"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var airports = []string{"DEL", "BOM", "BLR", "HYD", "CCU", "MAA", "GOI", "PNQ", "AMD", "JAI"}

// SeedIfEmpty seeds flights for the next 60 days if the flights table is empty.
func SeedIfEmpty(db *gorm.DB) {
	var count int64
	if err := db.Model(&models.Flight{}).Count(&count).Error; err != nil {
		log.Printf("seed check: %v", err)
		return
	}
	if count > 0 {
		return
	}
	log.Println("flights table empty, seeding...")
	Run(db)
}

// Run performs the full seed (used by cmd/seeder and by SeedIfEmpty).
func Run(db *gorm.DB) {
	now := time.Now().UTC()
	end := now.AddDate(0, 0, 60)
	var batch []*models.Flight
	for d := now; d.Before(end); d = d.AddDate(0, 0, 1) {
		dateStr := d.Format("2006-01-02")
		for _, origin := range airports {
			for _, dest := range airports {
				if origin == dest {
					continue
				}
				n := 3 + rand.Intn(3)
				for i := 0; i < n; i++ {
					depHour := 6 + rand.Intn(18)
					depMin := rand.Intn(60)
					dep := time.Date(d.Year(), d.Month(), d.Day(), depHour, depMin, 0, 0, time.UTC)
					dur := 60 + rand.Intn(180)
					arr := dep.Add(time.Duration(dur) * time.Minute)
					price := 2000 + rand.Float64()*13000
					f := &models.Flight{
						ID:            uuid.New(),
						CargoID:       "cargo-" + uuid.New().String()[:8],
						Origin:        origin,
						Destination:   dest,
						DepartureTime: dep,
						ArrivalTime:   arr,
						Price:         price,
						Status:        constants.FlightScheduled,
						ReservedSeats: pq.StringArray{},
						BookedSeats:   pq.StringArray{},
						TotalSeats:    constants.TotalSeatsPerFlight,
					}
					batch = append(batch, f)
					if len(batch) >= 500 {
						if err := db.CreateInBatches(batch, 500).Error; err != nil {
							log.Fatal("seed insert:", err)
						}
						log.Printf("seed: inserted %d flights up to %s", 500, dateStr)
						batch = batch[:0]
					}
				}
			}
		}
	}
	if len(batch) > 0 {
		if err := db.CreateInBatches(batch, 500).Error; err != nil {
			log.Fatal("seed insert:", err)
		}
	}
	log.Println("seed done")
}
