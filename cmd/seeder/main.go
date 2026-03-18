package main

import (
	"log"

	"flight-booking/internal/config"
	"flight-booking/internal/database"
	"flight-booking/internal/seed"
)

func main() {
	cfg := config.Load()
	db, err := database.Connect(cfg)
	if err != nil {
		log.Fatal("database:", err)
	}
	seed.Run(db)
}
