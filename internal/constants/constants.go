package constants

import "fmt"

type FlightStatus string

const (
	FlightScheduled   FlightStatus = "SCHEDULED"
	FlightFullyBooked FlightStatus = "FULLY_BOOKED"
	FlightDeparted    FlightStatus = "DEPARTED"
)

type BookingIntentStatus string

const (
	BookingPending       BookingIntentStatus = "PENDING"
	BookingPaymentFailed BookingIntentStatus = "PAYMENT_FAILED"
	BookingConfirmed     BookingIntentStatus = "CONFIRMED"
	BookingExpired       BookingIntentStatus = "EXPIRED"
)

func (s BookingIntentStatus) IsTerminal() bool {
	return s == BookingConfirmed || s == BookingExpired
}

func (s BookingIntentStatus) IsRetryable() bool {
	return s == BookingPending || s == BookingPaymentFailed
}

type PaymentIntentStatus string

const (
	PaymentInitiated PaymentIntentStatus = "INITIATED"
	PaymentSuccess   PaymentIntentStatus = "SUCCESS"
	PaymentFailed    PaymentIntentStatus = "FAILED"
	PaymentExpired   PaymentIntentStatus = "EXPIRED"
)

const (
	MaxRetries          = 3
	TotalSeatsPerFlight = 30
)

var AllSeats = generateSeats()

func generateSeats() []string {
	seats := make([]string, 0, 30)
	for _, row := range []string{"L", "M", "N"} {
		for i := 1; i <= 10; i++ {
			seats = append(seats, fmt.Sprintf("%s%d", row, i))
		}
	}
	return seats
}
