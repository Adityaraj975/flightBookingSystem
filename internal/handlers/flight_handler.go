package handlers

import (
	"net/http"
	"strconv"

	"flight-booking/internal/services"

	"github.com/gin-gonic/gin"
)

func ListFlights(flightSvc *services.FlightService) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Query("from")
		destination := c.Query("to")
		date := c.Query("date")
		if origin == "" || destination == "" || date == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from, to, and date are required"})
			return
		}
		var minPrice, maxPrice *float64
		if p := c.Query("minPrice"); p != "" {
			if v, err := strconv.ParseFloat(p, 64); err == nil {
				minPrice = &v
			}
		}
		if p := c.Query("maxPrice"); p != "" {
			if v, err := strconv.ParseFloat(p, 64); err == nil {
				maxPrice = &v
			}
		}
		flights, err := flightSvc.ListFlights(c.Request.Context(), origin, destination, date, minPrice, maxPrice)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		summaries := make([]gin.H, 0, len(flights))
		for _, f := range flights {
			avail := len(f.AvailableSeats())
			summaries = append(summaries, gin.H{
				"id":              f.ID.String(),
				"origin":          f.Origin,
				"destination":     f.Destination,
				"departureTime":   f.DepartureTime,
				"arrivalTime":     f.ArrivalTime,
				"price":           f.Price,
				"availableSeats":  avail,
				"status":          string(f.Status),
			})
		}
		c.JSON(http.StatusOK, gin.H{"flights": summaries, "count": len(summaries)})
	}
}

func GetFlight(flightSvc *services.FlightService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "id required"})
			return
		}
		f, err := flightSvc.GetFlight(c.Request.Context(), id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "flight not found"})
			return
		}
		available := f.AvailableSeats()
		c.JSON(http.StatusOK, gin.H{
			"id":             f.ID.String(),
			"origin":         f.Origin,
			"destination":    f.Destination,
			"departureTime":  f.DepartureTime,
			"arrivalTime":    f.ArrivalTime,
			"price":          f.Price,
			"status":         string(f.Status),
			"seatMap": gin.H{
				"available": available,
				"reserved":  f.ReservedSeats,
				"booked":    f.BookedSeats,
			},
		})
	}
}
