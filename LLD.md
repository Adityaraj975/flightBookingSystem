# Flight Booking System — Low-Level Design

## 1. Tech Stack

| Layer | Choice |
|---|---|
| Language | Go 1.22+ |
| HTTP Router | gin-gonic/gin |
| ORM | gorm.io/gorm + gorm.io/driver/postgres |
| Postgres | 16 (Docker) |
| Redis | 7 (Docker) |
| Kafka | Bitnami Kafka 3.7 with KRaft — no Zookeeper (Docker) |
| CDC | Postgres LISTEN/NOTIFY + a Go listener process |
| Kafka client | segmentio/kafka-go |
| Redis client | redis/go-redis/v9 |
| Postgres arrays | github.com/lib/pq (for `text[]` column mapping) |
| Config | environment variables loaded via joho/godotenv |

---

## 2. Project Structure

```
flight-booking/
├── cmd/
│   ├── api/                        # HTTP server — flights, bookings, webhook endpoints
│   │   └── main.go
│   ├── booking-worker/             # Kafka consumer — processes payment callbacks
│   │   └── main.go
│   ├── cron-worker/                # Periodic jobs — cleanup, hot key refresh, flight ingestion
│   │   └── main.go
│   ├── cdc-worker/                 # Postgres LISTEN/NOTIFY → Redis sync
│   │   └── main.go
│   ├── payment-server/             # Dummy payment gateway — simulates async callbacks
│   │   └── main.go
│   └── seeder/                     # One-time seed script — populate initial flight data
│       └── main.go
├── internal/
│   ├── config/
│   │   └── config.go               # Loads .env, exposes typed config struct
│   ├── constants/
│   │   └── constants.go            # Enums, max retries, timeouts
│   ├── models/
│   │   ├── flight.go
│   │   ├── booking_intent.go
│   │   ├── payment_intent.go
│   │   └── booking_intent_cleanup.go
│   ├── database/
│   │   └── postgres.go             # GORM connection, auto-migrate
│   ├── cache/
│   │   └── redis.go                # Redis client, flight cache ops, metrics ops
│   ├── queue/
│   │   ├── producer.go             # Kafka producer (used by webhook handler)
│   │   └── consumer.go             # Kafka consumer (used by booking worker)
│   ├── handlers/
│   │   ├── flight_handler.go       # GET /api/flights, GET /api/flights/:id
│   │   ├── booking_handler.go      # POST /api/bookings, GET /api/bookings/:id, POST retry
│   │   └── webhook_handler.go      # POST /webhooks/payment
│   ├── services/
│   │   ├── flight_service.go       # Flight listing, Redis cache-aside, hot key reads
│   │   ├── booking_service.go      # Seat reservation, payment initiation, retry logic
│   │   ├── callback_service.go     # Payment callback processing (decision tree from HLD §4.5)
│   │   └── cleanup_service.go      # Booking expiry logic (used by cron worker)
│   └── cdc/
│       └── listener.go             # Postgres LISTEN/NOTIFY handler → Redis update
├── migrations/
│   └── 001_initial.up.sql          # DDL: tables, indexes, enums, triggers
├── docker-compose.yml
├── Makefile
├── Procfile                        # For running all services locally
├── go.mod
├── go.sum
├── .env.example
├── HLD.md
└── LLD.md
```

---

## 3. Docker Infrastructure

### 3.1 docker-compose.yml

```yaml
version: "3.9"

services:
  postgres:
    image: postgres:16-alpine
    container_name: fb-postgres
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: flight_user
      POSTGRES_PASSWORD: flight_pass
      POSTGRES_DB: flight_booking
    volumes:
      - pg_data:/var/lib/postgresql/data
      - ./migrations:/docker-entrypoint-initdb.d
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U flight_user -d flight_booking"]
      interval: 5s
      timeout: 5s
      retries: 5

  redis:
    image: redis:7-alpine
    container_name: fb-redis
    ports:
      - "6379:6379"
    command: redis-server --maxmemory 256mb --maxmemory-policy allkeys-lru
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 5

  kafka:
    image: bitnami/kafka:3.7
    container_name: fb-kafka
    ports:
      - "9092:9092"
    environment:
      KAFKA_CFG_NODE_ID: 1
      KAFKA_CFG_PROCESS_ROLES: broker,controller
      KAFKA_CFG_CONTROLLER_QUORUM_VOTERS: 1@kafka:9093
      KAFKA_CFG_LISTENERS: PLAINTEXT://:9092,CONTROLLER://:9093
      KAFKA_CFG_ADVERTISED_LISTENERS: PLAINTEXT://localhost:9092
      KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP: CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT
      KAFKA_CFG_CONTROLLER_LISTENER_NAMES: CONTROLLER
      KAFKA_CFG_AUTO_CREATE_TOPICS_ENABLE: "true"
    volumes:
      - kafka_data:/bitnami/kafka
    healthcheck:
      test: ["CMD-SHELL", "kafka-topics.sh --bootstrap-server localhost:9092 --list"]
      interval: 10s
      timeout: 10s
      retries: 5

volumes:
  pg_data:
  kafka_data:
```

### 3.2 .env.example

```env
# Postgres
DB_HOST=localhost
DB_PORT=5432
DB_USER=flight_user
DB_PASSWORD=flight_pass
DB_NAME=flight_booking
DB_SSLMODE=disable

# Redis
REDIS_ADDR=localhost:6379

# Kafka
KAFKA_BROKERS=localhost:9092
KAFKA_TOPIC_PAYMENT_CALLBACKS=payment.callbacks
KAFKA_CONSUMER_GROUP=booking-worker-group

# API Server
API_PORT=8080

# Dummy Payment Server
PAYMENT_SERVER_PORT=8081
PAYMENT_CALLBACK_BASE_URL=http://localhost:8080/webhooks/payment
PAYMENT_MIN_DELAY_SEC=5
PAYMENT_MAX_DELAY_SEC=120
PAYMENT_SUCCESS_RATE=80

# Booking
INITIAL_RESERVATION_MINUTES=10
RETRY_EXTENSION_MINUTES=2
MAX_RETRIES=3

# Hot Keys
SOURCE_THRESHOLD=500
DESTINATION_THRESHOLD=500
HOT_KEY_TTL_SEC=300
METRIC_TTL_SEC=86400

# Cron
CLEANUP_INTERVAL_SEC=60
HOT_KEY_REFRESH_INTERVAL_SEC=300
```

---

## 4. Database Schema

### 4.1 SQL Migration — `migrations/001_initial.up.sql`

```sql
-- Enums
CREATE TYPE flight_status AS ENUM ('SCHEDULED', 'FULLY_BOOKED', 'DEPARTED');
CREATE TYPE booking_intent_status AS ENUM ('PENDING', 'PAYMENT_FAILED', 'CONFIRMED', 'EXPIRED');
CREATE TYPE payment_intent_status AS ENUM ('INITIATED', 'SUCCESS', 'FAILED', 'EXPIRED');

-- Flights
CREATE TABLE flights (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    cargo_id          VARCHAR(64) NOT NULL,
    origin            VARCHAR(10) NOT NULL,
    destination       VARCHAR(10) NOT NULL,
    departure_time    TIMESTAMPTZ NOT NULL,
    arrival_time      TIMESTAMPTZ NOT NULL,
    price             DECIMAL(10, 2) NOT NULL,
    status            flight_status NOT NULL DEFAULT 'SCHEDULED',
    reserved_seats    TEXT[] NOT NULL DEFAULT '{}',
    booked_seats      TEXT[] NOT NULL DEFAULT '{}',
    total_seats       INT NOT NULL DEFAULT 30,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_flights_origin_dest_date ON flights (origin, destination, (departure_time::date));
CREATE INDEX idx_flights_origin_date ON flights (origin, (departure_time::date));
CREATE INDEX idx_flights_dest_date ON flights (destination, (departure_time::date));
CREATE INDEX idx_flights_status ON flights (status);
CREATE INDEX idx_flights_departure ON flights (departure_time);

-- Booking Intents
CREATE TABLE booking_intents (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_intent_id          VARCHAR(64) UNIQUE NOT NULL,
    user_id                    UUID NOT NULL,
    flight_id                  UUID NOT NULL REFERENCES flights(id),
    seats                      TEXT[] NOT NULL,
    customer_details           JSONB NOT NULL DEFAULT '{}',
    active_payment_intent_id   VARCHAR(64),
    status                     booking_intent_status NOT NULL DEFAULT 'PENDING',
    retry_count                INT NOT NULL DEFAULT 0,
    metadata                   JSONB NOT NULL DEFAULT '{}',
    expires_at                 TIMESTAMPTZ NOT NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_booking_intents_status ON booking_intents (status);
CREATE INDEX idx_booking_intents_booking_id ON booking_intents (booking_intent_id);
CREATE INDEX idx_booking_intents_flight ON booking_intents (flight_id);

-- Payment Intents
CREATE TABLE payment_intents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_intent_id   VARCHAR(64) UNIQUE NOT NULL,
    booking_intent_id   VARCHAR(64) NOT NULL,
    amount              DECIMAL(10, 2) NOT NULL,
    status              payment_intent_status NOT NULL DEFAULT 'INITIATED',
    metadata            JSONB NOT NULL DEFAULT '{}',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_payment_intents_payment_id ON payment_intents (payment_intent_id);
CREATE INDEX idx_payment_intents_booking_id ON payment_intents (booking_intent_id);
CREATE INDEX idx_payment_intents_status ON payment_intents (status);

-- Booking Intent Cleanup
CREATE TABLE booking_intent_cleanup (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_intent_id   VARCHAR(64) UNIQUE NOT NULL,
    expiry              TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_cleanup_expiry ON booking_intent_cleanup (expiry);

-- Updated_at auto-update trigger
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_flights_updated_at
    BEFORE UPDATE ON flights FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_booking_intents_updated_at
    BEFORE UPDATE ON booking_intents FOR EACH ROW EXECUTE FUNCTION update_updated_at();
CREATE TRIGGER trg_payment_intents_updated_at
    BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- CDC: Notify on flight changes (for Redis sync)
CREATE OR REPLACE FUNCTION notify_flight_changes()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('flight_changes', json_build_object(
        'op', TG_OP,
        'flight_id', NEW.id,
        'origin', NEW.origin,
        'destination', NEW.destination,
        'departure_date', to_char(NEW.departure_time AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
        'status', NEW.status,
        'reserved_seats', array_to_json(NEW.reserved_seats),
        'booked_seats', array_to_json(NEW.booked_seats)
    )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_flight_cdc
    AFTER INSERT OR UPDATE ON flights
    FOR EACH ROW EXECUTE FUNCTION notify_flight_changes();
```

---

## 5. GORM Models

### 5.1 `internal/constants/constants.go`

```go
package constants

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
    MaxRetries               = 3
    InitialReservationMin    = 10
    RetryExtensionMin        = 2
    TotalSeatsPerFlight      = 30
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
```

### 5.2 `internal/models/flight.go`

```go
package models

import (
    "time"

    "github.com/google/uuid"
    "github.com/lib/pq"
    "flight-booking/internal/constants"
)

type Flight struct {
    ID            uuid.UUID              `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    CargoID       string                 `gorm:"type:varchar(64);not null" json:"cargoId"`
    Origin        string                 `gorm:"type:varchar(10);not null" json:"origin"`
    Destination   string                 `gorm:"type:varchar(10);not null" json:"destination"`
    DepartureTime time.Time              `gorm:"type:timestamptz;not null" json:"departureTime"`
    ArrivalTime   time.Time              `gorm:"type:timestamptz;not null" json:"arrivalTime"`
    Price         float64                `gorm:"type:decimal(10,2);not null" json:"price"`
    Status        constants.FlightStatus `gorm:"type:flight_status;not null;default:'SCHEDULED'" json:"status"`
    ReservedSeats pq.StringArray         `gorm:"type:text[];not null;default:'{}'" json:"reservedSeats"`
    BookedSeats   pq.StringArray         `gorm:"type:text[];not null;default:'{}'" json:"bookedSeats"`
    TotalSeats    int                    `gorm:"type:int;not null;default:30" json:"totalSeats"`
    CreatedAt     time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
    UpdatedAt     time.Time              `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
}

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
```

### 5.3 `internal/models/booking_intent.go`

```go
package models

import (
    "time"

    "github.com/google/uuid"
    "github.com/lib/pq"
    "gorm.io/datatypes"
    "flight-booking/internal/constants"
)

type BookingIntent struct {
    ID                     uuid.UUID                      `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    BookingIntentID        string                         `gorm:"type:varchar(64);uniqueIndex;not null" json:"bookingIntentId"`
    UserID                 uuid.UUID                      `gorm:"type:uuid;not null" json:"userId"`
    FlightID               uuid.UUID                      `gorm:"type:uuid;not null" json:"flightId"`
    Seats                  pq.StringArray                 `gorm:"type:text[];not null" json:"seats"`
    CustomerDetails        datatypes.JSON                 `gorm:"type:jsonb;not null;default:'{}'" json:"customerDetails"`
    ActivePaymentIntentID  string                         `gorm:"type:varchar(64)" json:"activePaymentIntentId"`
    Status                 constants.BookingIntentStatus  `gorm:"type:booking_intent_status;not null;default:'PENDING'" json:"status"`
    RetryCount             int                            `gorm:"type:int;not null;default:0" json:"retryCount"`
    Metadata               datatypes.JSON                 `gorm:"type:jsonb;not null;default:'{}'" json:"metadata"`
    ExpiresAt              time.Time                      `gorm:"type:timestamptz;not null" json:"expiresAt"`
    CreatedAt              time.Time                      `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
    UpdatedAt              time.Time                      `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`

    Flight                 Flight                         `gorm:"foreignKey:FlightID" json:"flight,omitempty"`
}
```

### 5.4 `internal/models/payment_intent.go`

```go
package models

import (
    "time"

    "github.com/google/uuid"
    "gorm.io/datatypes"
    "flight-booking/internal/constants"
)

type PaymentIntent struct {
    ID               uuid.UUID                       `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    PaymentIntentID  string                          `gorm:"type:varchar(64);uniqueIndex;not null" json:"paymentIntentId"`
    BookingIntentID  string                          `gorm:"type:varchar(64);not null;index" json:"bookingIntentId"`
    Amount           float64                         `gorm:"type:decimal(10,2);not null" json:"amount"`
    Status           constants.PaymentIntentStatus   `gorm:"type:payment_intent_status;not null;default:'INITIATED'" json:"status"`
    Metadata         datatypes.JSON                  `gorm:"type:jsonb;not null;default:'{}'" json:"metadata"`
    CreatedAt        time.Time                       `gorm:"type:timestamptz;not null;default:now()" json:"createdAt"`
    UpdatedAt        time.Time                       `gorm:"type:timestamptz;not null;default:now()" json:"updatedAt"`
}
```

### 5.5 `internal/models/booking_intent_cleanup.go`

```go
package models

import (
    "github.com/google/uuid"
    "time"
)

type BookingIntentCleanup struct {
    ID              uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    BookingIntentID string    `gorm:"type:varchar(64);uniqueIndex;not null" json:"bookingIntentId"`
    Expiry          time.Time `gorm:"type:timestamptz;not null;index" json:"expiry"`
}
```

---

## 6. Service Layer — Core Business Logic

### 6.1 Booking Service — `internal/services/booking_service.go`

**CreateBooking(flightID, seats, userID, customerDetails):**

```
func CreateBooking:
    1. Generate bookingIntentID = "bi_" + uuid
    2. Generate paymentIntentID = "pi_" + uuid
    3. expiresAt = time.Now().Add(INITIAL_RESERVATION_MINUTES * time.Minute)

    4. BEGIN transaction (serializable isolation not needed — FOR UPDATE is sufficient)

    5. SELECT * FROM flights WHERE id = flightID FOR UPDATE
       - If flight.Status != SCHEDULED → return error "flight not available"
       - Build taken = union(flight.ReservedSeats, flight.BookedSeats)
       - For each seat in request.seats:
           if seat in taken → ROLLBACK, return error "seat {seat} unavailable"

    6. UPDATE flights SET reserved_seats = array_cat(reserved_seats, requestedSeats)
       WHERE id = flightID

    7. INSERT INTO booking_intents (
           booking_intent_id, user_id, flight_id, seats, customer_details,
           active_payment_intent_id, status, retry_count, expires_at
       ) VALUES (
           bookingIntentID, userID, flightID, seats, customerDetails,
           paymentIntentID, 'PENDING', 0, expiresAt
       )

    8. INSERT INTO payment_intents (
           payment_intent_id, booking_intent_id, amount, status
       ) VALUES (
           paymentIntentID, bookingIntentID, flight.Price * len(seats), 'INITIATED'
       )

    9. INSERT INTO booking_intent_cleanup (booking_intent_id, expiry)
       VALUES (bookingIntentID, expiresAt)

   10. COMMIT

   11. POST to Dummy Payment Server (async, fire-and-forget goroutine):
       {
           paymentIntentId: paymentIntentID,
           amount: flight.Price * len(seats),
           currency: "INR",
           callbackUrl: PAYMENT_CALLBACK_BASE_URL
       }

   12. Return { bookingIntentId, status: "PENDING", expiresAt, seats }
```

**RetryPayment(bookingIntentID):**

```
func RetryPayment:
    1. Generate newPaymentIntentID = "pi_" + uuid

    2. BEGIN transaction

    3. SELECT * FROM booking_intents WHERE booking_intent_id = bookingIntentID FOR UPDATE
       - If status.IsTerminal() → return error "booking is {status}, cannot retry"
       - If !status.IsRetryable() → return error "cannot retry in current state"
       - If retry_count >= MAX_RETRIES → return error "max retries exhausted"

    4. newExpiresAt = time.Now().Add(RETRY_EXTENSION_MINUTES * time.Minute)

    5. UPDATE booking_intents SET
           active_payment_intent_id = newPaymentIntentID,
           retry_count = retry_count + 1,
           status = 'PENDING',
           expires_at = newExpiresAt

    6. INSERT INTO payment_intents (
           payment_intent_id, booking_intent_id, amount, status
       ) VALUES (newPaymentIntentID, bookingIntentID, originalAmount, 'INITIATED')

    7. UPDATE booking_intent_cleanup SET expiry = newExpiresAt
       WHERE booking_intent_id = bookingIntentID

    8. COMMIT

    9. POST to Dummy Payment Server (async):
       { paymentIntentId: newPaymentIntentID, amount, callbackUrl }

   10. Return { bookingIntentId, status: "PENDING", expiresAt: newExpiresAt,
                retryCount, retriesRemaining: MAX_RETRIES - retryCount }
```

### 6.2 Callback Service — `internal/services/callback_service.go`

This implements the decision tree from HLD §4.5. Called by the Booking Worker on each Kafka message.

```
func ProcessPaymentCallback(paymentIntentID string, callbackStatus string):

    1. SELECT * FROM payment_intents WHERE payment_intent_id = paymentIntentID
       - If not found → log warning, return (orphan callback)
       - Get bookingIntentID from this row

    2. BEGIN transaction

    3. SELECT * FROM booking_intents
       WHERE booking_intent_id = bookingIntentID FOR UPDATE
       → bookingIntent

    4. SELECT * FROM flights WHERE id = bookingIntent.FlightID FOR UPDATE
       → flight  (lock flight row too — we may modify seats)

    ──────────────────────────────────────────────────────────
    CASE: bookingIntent.Status is terminal (CONFIRMED or EXPIRED)
    ──────────────────────────────────────────────────────────

        IF callbackStatus == "SUCCESS":
            UPDATE payment_intents SET status = 'SUCCESS'
              WHERE payment_intent_id = paymentIntentID
            → ENQUEUE REFUND for paymentIntentID
            COMMIT
            return

        IF callbackStatus == "FAILED":
            UPDATE payment_intents SET status = 'FAILED'
              WHERE payment_intent_id = paymentIntentID
            COMMIT
            return

    ──────────────────────────────────────────────────────────
    CASE: bookingIntent.Status is non-terminal (PENDING or PAYMENT_FAILED)
    ──────────────────────────────────────────────────────────

        isActivePI = (paymentIntentID == bookingIntent.ActivePaymentIntentID)

        IF isActivePI AND callbackStatus == "SUCCESS":
            // ✅ Happy path — confirm booking
            UPDATE booking_intents SET status = 'CONFIRMED'
            UPDATE payment_intents SET status = 'SUCCESS'
              WHERE payment_intent_id = paymentIntentID

            // Move seats: reserved → booked
            newReserved = remove(flight.ReservedSeats, bookingIntent.Seats)
            newBooked   = append(flight.BookedSeats, bookingIntent.Seats...)
            UPDATE flights SET
              reserved_seats = newReserved,
              booked_seats = newBooked,
              status = CASE WHEN len(newBooked) >= 30 THEN 'FULLY_BOOKED' ELSE status END

            // Expire all other INITIATED PIs for this booking
            UPDATE payment_intents SET status = 'EXPIRED'
              WHERE booking_intent_id = bookingIntentID
              AND status = 'INITIATED'
              AND payment_intent_id != paymentIntentID

            DELETE FROM booking_intent_cleanup
              WHERE booking_intent_id = bookingIntentID

            COMMIT
            return

        IF isActivePI AND callbackStatus == "FAILED":
            UPDATE booking_intents SET status = 'PAYMENT_FAILED'
            UPDATE payment_intents SET status = 'FAILED'
              WHERE payment_intent_id = paymentIntentID
            COMMIT
            return

        IF !isActivePI AND callbackStatus == "SUCCESS":
            // Stale payment succeeded — refund it
            UPDATE payment_intents SET status = 'SUCCESS'
              WHERE payment_intent_id = paymentIntentID
            → ENQUEUE REFUND for paymentIntentID
            COMMIT
            return

        IF !isActivePI AND callbackStatus == "FAILED":
            // Stale payment failed — audit only
            UPDATE payment_intents SET status = 'FAILED'
              WHERE payment_intent_id = paymentIntentID
            COMMIT
            return
```

### 6.3 Cleanup Service — `internal/services/cleanup_service.go`

Called by the Cron Worker every 60 seconds.

```
func RunCleanup(batchSize int):

    1. SELECT * FROM booking_intent_cleanup
       WHERE expiry <= NOW()
       ORDER BY expiry ASC
       LIMIT batchSize                         -- outside transaction, just a scan

    2. For each cleanup row:
       a. BEGIN transaction

       b. SELECT * FROM booking_intents
          WHERE booking_intent_id = cleanup.BookingIntentID
          FOR UPDATE → bookingIntent

       c. IF bookingIntent.Status.IsTerminal():
            DELETE FROM booking_intent_cleanup WHERE id = cleanup.ID
            COMMIT
            continue

       d. // Non-terminal — but is it truly expired?
          IF bookingIntent.ExpiresAt.After(time.Now()):
            // Retry extended the window — sync cleanup table, skip
            UPDATE booking_intent_cleanup SET expiry = bookingIntent.ExpiresAt
              WHERE id = cleanup.ID
            COMMIT
            continue

       e. // Truly expired — clean up
          SELECT * FROM flights WHERE id = bookingIntent.FlightID FOR UPDATE → flight

          UPDATE booking_intents SET status = 'EXPIRED'

          UPDATE payment_intents SET status = 'EXPIRED'
            WHERE booking_intent_id = bookingIntent.BookingIntentID
            AND status = 'INITIATED'

          newReserved = remove(flight.ReservedSeats, bookingIntent.Seats)
          UPDATE flights SET reserved_seats = newReserved
          IF flight.Status == 'FULLY_BOOKED' AND len(newReserved) + len(flight.BookedSeats) < 30:
            UPDATE flights SET status = 'SCHEDULED'

          DELETE FROM booking_intent_cleanup WHERE id = cleanup.ID

          COMMIT
```

### 6.4 Flight Service — `internal/services/flight_service.go`

```
func ListFlights(origin, destination, date string, filters ...):

    // Step 1: Increment search metrics (fire-and-forget)
    go redis.Incr("metrics:search:from:" + origin + ":" + date)
    go redis.Incr("metrics:search:to:" + destination + ":" + date)
    go redis.Incr("metrics:search:route:" + origin + ":" + destination + ":" + date)

    // Step 2: Try hot source key
    hotKey = "flights:hot:from:" + origin + ":" + date
    if redis.SIsMember("hot:sources:" + date, origin):
        flights = redis.Get(hotKey)  // returns all flights from this origin on this date
        if flights != nil:
            return filterInMemory(flights, destination, filters)

    // Step 3: Try route-level cache
    routeKey = "flights:route:" + origin + ":" + destination + ":" + date
    flights = redis.Get(routeKey)
    if flights != nil:
        return filterInMemory(flights, "", filters)  // already filtered by dest

    // Step 4: Cache miss — query Postgres
    flights = db.Where("origin = ? AND destination = ? AND departure_time::date = ? AND status = 'SCHEDULED'",
                        origin, destination, date).Find(&flights)

    // Populate cache (set TTL to end of departure date)
    ttl = endOfDay(date).Sub(time.Now())
    redis.Set(routeKey, serialize(flights), ttl)

    return filterInMemory(flights, "", filters)
```

---

## 7. CDC — Postgres LISTEN/NOTIFY → Redis

### 7.1 How It Works

```
┌──────────────┐    trigger fires on     ┌─────────────────┐    NOTIFY     ┌────────────┐
│  Booking     │──▶ INSERT/UPDATE ──────▶│  Postgres        │────────────▶│ CDC Worker  │
│  Worker /    │    on flights table      │  trg_flight_cdc  │  channel:    │ (Go process)│
│  API Server  │                         └─────────────────┘  flight_      │             │
└──────────────┘                                               changes     └──────┬──────┘
                                                                                  │
                                                                    parses JSON payload
                                                                    updates Redis keys
                                                                                  │
                                                                                  ▼
                                                                           ┌────────────┐
                                                                           │   Redis     │
                                                                           └────────────┘
```

### 7.2 CDC Worker — `internal/cdc/listener.go`

```
func StartCDCListener(pgConnString string, redisClient *redis.Client):

    conn = pgx.Connect(pgConnString)    // raw pgx, not GORM — needed for LISTEN
    conn.Exec("LISTEN flight_changes")

    for {
        notification = conn.WaitForNotification(context.Background())
        payload = json.Unmarshal(notification.Payload) → CDCPayload

        // payload contains: op, flight_id, origin, destination, departure_date,
        //                    status, reserved_seats, booked_seats

        // Update route-level cache key if it exists
        routeKey = "flights:route:" + payload.Origin + ":" + payload.Destination + ":" + payload.DepartureDate
        if redis.Exists(routeKey):
            updateFlightInCachedList(redisClient, routeKey, payload)

        // Update hot source key if origin is hot
        if redis.SIsMember("hot:sources:" + payload.DepartureDate, payload.Origin):
            hotFromKey = "flights:hot:from:" + payload.Origin + ":" + payload.DepartureDate
            updateFlightInCachedList(redisClient, hotFromKey, payload)

        // Update hot destination key if destination is hot
        if redis.SIsMember("hot:destinations:" + payload.DepartureDate, payload.Destination):
            hotToKey = "flights:hot:to:" + payload.Destination + ":" + payload.DepartureDate
            updateFlightInCachedList(redisClient, hotToKey, payload)
    }

func updateFlightInCachedList(redis, key, payload):
    // GET the current list, find the flight by ID, update its fields
    // (reserved_seats, booked_seats, status), SET back
    // If flight not in list and op == INSERT, append it
    // If flight status == DEPARTED, remove it from list
```

### 7.3 Postgres LISTEN/NOTIFY — Limitations & Mitigations

| Limitation | Mitigation |
|---|---|
| Notifications are lost if CDC worker is down | Cron Worker periodically refreshes hot keys from Postgres (full reconciliation every 5 min) |
| Payload max 8000 bytes | Our payload is small (~200 bytes) — well within limits |
| No replay / no durability | Acceptable for cache — stale data resolves on next refresh or cache miss |
| Single listener per connection | One CDC worker process is sufficient; scale with multiple if needed |

---

## 8. Kafka Design

### 8.1 Topic

| Topic | Partitions | Replication | Key | Value |
|---|---|---|---|---|
| `payment.callbacks` | 3 | 1 (local dev) | `paymentIntentId` | `{ paymentIntentId, status, paidAt? }` |

Using `paymentIntentId` as partition key distributes load evenly. Ordering per-payment is guaranteed within a partition, and our `FOR UPDATE` locks handle cross-payment ordering for the same booking.

### 8.2 Producer — `internal/queue/producer.go`

Used by the Webhook handler to publish payment callback events.

```go
type PaymentCallbackEvent struct {
    PaymentIntentID string `json:"paymentIntentId"`
    Status          string `json:"status"`
    PaidAt          string `json:"paidAt,omitempty"`
}

func (p *Producer) PublishPaymentCallback(event PaymentCallbackEvent) error {
    value, _ := json.Marshal(event)
    return p.writer.WriteMessages(ctx, kafka.Message{
        Key:   []byte(event.PaymentIntentID),
        Value: value,
    })
}
```

### 8.3 Consumer — `internal/queue/consumer.go`

Used by the Booking Worker to consume and process callbacks.

```go
func (c *Consumer) Start(ctx context.Context, handler func(PaymentCallbackEvent) error) {
    for {
        msg, err := c.reader.ReadMessage(ctx)
        if err != nil { break }

        var event PaymentCallbackEvent
        json.Unmarshal(msg.Value, &event)

        if err := handler(event); err != nil {
            log.Error("failed to process callback", "pi", event.PaymentIntentID, "err", err)
            // Message is not committed — will be retried
            continue
        }
        // Auto-commit on successful processing (reader config: CommitMessages)
    }
}
```

---

## 9. Dummy Payment Server — `cmd/payment-server/main.go`

Runs as a separate process on port 8081.

### 9.1 Endpoint

```
POST /create-payment
```

### 9.2 Logic

```
func HandleCreatePayment(c *gin.Context):
    var req struct {
        PaymentIntentID string  `json:"paymentIntentId"`
        Amount          float64 `json:"amount"`
        Currency        string  `json:"currency"`
        CallbackURL     string  `json:"callbackUrl"`
    }
    c.BindJSON(&req)

    // Respond immediately
    c.JSON(200, gin.H{"status": "ACKNOWLEDGED", "paymentIntentId": req.PaymentIntentID})

    // Schedule async callback
    go func() {
        delay = randomDuration(PAYMENT_MIN_DELAY, PAYMENT_MAX_DELAY)
        time.Sleep(delay)

        status = weightedRandom(PAYMENT_SUCCESS_RATE)  // "SUCCESS" or "FAILED"

        payload = map[string]any{
            "paymentIntentId": req.PaymentIntentID,
            "status":          status,
            "paidAt":          time.Now().UTC().Format(time.RFC3339),
        }

        resp, err = http.Post(req.CallbackURL, "application/json", toJSON(payload))
        if err != nil {
            log.Warn("callback delivery failed, scheduling retry", "pi", req.PaymentIntentID)
            // Retry once after 5 seconds (simulates webhook retry)
            time.Sleep(5 * time.Second)
            http.Post(req.CallbackURL, "application/json", toJSON(payload))
        }
    }()
```

### 9.3 Configurable Behaviors (via env vars)

| Env Var | Default | Purpose |
|---|---|---|
| `PAYMENT_MIN_DELAY_SEC` | 5 | Minimum callback delay (seconds) |
| `PAYMENT_MAX_DELAY_SEC` | 120 | Maximum callback delay (seconds) |
| `PAYMENT_SUCCESS_RATE` | 80 | Percentage of callbacks that return SUCCESS |
| `PAYMENT_DUPLICATE_RATE` | 10 | Percentage of callbacks sent twice (tests idempotency) |
| `PAYMENT_NO_CALLBACK_RATE` | 5 | Percentage that never send a callback (tests expiry) |

---

## 10. HTTP Handlers

### 10.1 Router Setup — `cmd/api/main.go`

```go
func main() {
    cfg := config.Load()
    db := database.Connect(cfg)
    rdb := cache.NewRedisClient(cfg)
    producer := queue.NewProducer(cfg)

    flightSvc := services.NewFlightService(db, rdb)
    bookingSvc := services.NewBookingService(db, rdb, cfg)
    callbackSvc := services.NewCallbackService(db, rdb)

    r := gin.Default()

    api := r.Group("/api")
    {
        api.GET("/flights", handlers.ListFlights(flightSvc))
        api.GET("/flights/:id", handlers.GetFlight(flightSvc))
        api.POST("/bookings", handlers.CreateBooking(bookingSvc))
        api.GET("/bookings/:id", handlers.GetBooking(bookingSvc))
        api.POST("/bookings/:id/retry", handlers.RetryPayment(bookingSvc))
    }

    r.POST("/webhooks/payment", handlers.HandlePaymentWebhook(producer))

    r.Run(":" + cfg.APIPort)
}
```

### 10.2 Request / Response Types

**POST /api/bookings**

```go
// Request
type CreateBookingRequest struct {
    FlightID        string            `json:"flightId" binding:"required"`
    Seats           []string          `json:"seats" binding:"required,min=1"`
    UserID          string            `json:"userId" binding:"required"`
    CustomerDetails map[string]any    `json:"customerDetails" binding:"required"`
}

// Response — 201 Created
type CreateBookingResponse struct {
    BookingIntentID string    `json:"bookingIntentId"`
    Status          string    `json:"status"`
    Seats           []string  `json:"seats"`
    ExpiresAt       time.Time `json:"expiresAt"`
    RetryCount      int       `json:"retryCount"`
    MaxRetries      int       `json:"maxRetries"`
}
```

**GET /api/bookings/:id**

```go
// Response
type GetBookingResponse struct {
    BookingIntentID       string    `json:"bookingIntentId"`
    FlightID              string    `json:"flightId"`
    Seats                 []string  `json:"seats"`
    Status                string    `json:"status"`
    ActivePaymentIntentID string    `json:"activePaymentIntentId"`
    RetryCount            int       `json:"retryCount"`
    MaxRetries            int       `json:"maxRetries"`
    RetriesRemaining      int       `json:"retriesRemaining"`
    ExpiresAt             time.Time `json:"expiresAt"`
    CreatedAt             time.Time `json:"createdAt"`
}
```

**POST /api/bookings/:id/retry**

```go
// Response — 200 OK
type RetryPaymentResponse struct {
    BookingIntentID    string    `json:"bookingIntentId"`
    NewPaymentIntentID string    `json:"newPaymentIntentId"`
    Status             string    `json:"status"`
    RetryCount         int       `json:"retryCount"`
    RetriesRemaining   int       `json:"retriesRemaining"`
    ExpiresAt          time.Time `json:"expiresAt"`
}

// Response — 400 Bad Request (max retries)
// { "error": "max retries exhausted (3/3)" }

// Response — 409 Conflict (terminal state)
// { "error": "booking is CONFIRMED, cannot retry" }
```

**GET /api/flights?from=DEL&to=BOM&date=2026-03-25**

```go
// Response
type FlightListResponse struct {
    Flights []FlightSummary `json:"flights"`
    Count   int             `json:"count"`
}

type FlightSummary struct {
    ID             string    `json:"id"`
    Origin         string    `json:"origin"`
    Destination    string    `json:"destination"`
    DepartureTime  time.Time `json:"departureTime"`
    ArrivalTime    time.Time `json:"arrivalTime"`
    Price          float64   `json:"price"`
    AvailableSeats int       `json:"availableSeats"`
    Status         string    `json:"status"`
}
```

**GET /api/flights/:id**

```go
// Response
type FlightDetailResponse struct {
    ID             string        `json:"id"`
    Origin         string        `json:"origin"`
    Destination    string        `json:"destination"`
    DepartureTime  time.Time     `json:"departureTime"`
    ArrivalTime    time.Time     `json:"arrivalTime"`
    Price          float64       `json:"price"`
    Status         string        `json:"status"`
    SeatMap        SeatMap       `json:"seatMap"`
}

type SeatMap struct {
    Available []string `json:"available"`
    Reserved  []string `json:"reserved"`
    Booked    []string `json:"booked"`
}
```

**POST /webhooks/payment**

```go
// Request (from Dummy Payment Server or real gateway)
type PaymentWebhookRequest struct {
    PaymentIntentID string `json:"paymentIntentId" binding:"required"`
    Status          string `json:"status" binding:"required"` // "SUCCESS" or "FAILED"
    PaidAt          string `json:"paidAt,omitempty"`
}

// Response — always 200 OK (ACK the webhook, process async via Kafka)
// { "received": true }
```

---

## 11. Entry Points — `cmd/` Processes

### 11.1 API Server (`cmd/api/main.go`)

```
- Starts Gin HTTP server on :8080
- Registers routes: /api/flights, /api/bookings, /webhooks/payment
- Dependencies: Postgres (GORM), Redis, Kafka producer
```

### 11.2 Booking Worker (`cmd/booking-worker/main.go`)

```
- Starts Kafka consumer on topic: payment.callbacks, group: booking-worker-group
- On each message: calls callbackService.ProcessPaymentCallback(piID, status)
- Dependencies: Postgres (GORM), Redis, Kafka consumer
```

### 11.3 Cron Worker (`cmd/cron-worker/main.go`)

```
- Runs three periodic loops (each in its own goroutine):

  1. Cleanup loop (every 60s):
     cleanupService.RunCleanup(batchSize=100)

  2. Hot key refresh loop (every 300s):
     - Scan metrics:search:from:* keys → find origins above SOURCE_THRESHOLD
     - Update hot:sources:{date} set
     - For each hot origin+date: query Postgres, write flights:hot:from:{origin}:{date}
     - Same for destinations

  3. Flight departure sweep (every 300s):
     - UPDATE flights SET status = 'DEPARTED'
       WHERE departure_time < NOW() AND status != 'DEPARTED'

- Dependencies: Postgres (GORM), Redis
```

### 11.4 CDC Worker (`cmd/cdc-worker/main.go`)

```
- Opens raw pgx connection (not GORM — needed for LISTEN/NOTIFY)
- Runs: LISTEN flight_changes
- On each notification: parses payload, updates Redis cache keys
- Dependencies: Postgres (pgx), Redis
```

### 11.5 Payment Server (`cmd/payment-server/main.go`)

```
- Starts Gin HTTP server on :8081
- Single endpoint: POST /create-payment
- On request: responds 200 immediately, schedules goroutine with random delay
- After delay: POSTs callback to callbackUrl
- Dependencies: none (standalone)
```

### 11.6 Seeder (`cmd/seeder/main.go`)

```
- Run once to populate initial flight data
- Generates flights for the next 60 days
- Origins: DEL, BOM, BLR, HYD, CCU, MAA, GOI, PNQ, AMD, JAI
- Destinations: same pool (excluding origin)
- ~8 flights per route per day (randomized)
- Price: random between ₹2000–₹15000
- Departure times: random between 06:00–23:00
- Dependencies: Postgres (GORM)
```

---

## 12. Makefile

```makefile
.PHONY: infra-up infra-down migrate seed api booking-worker cron-worker cdc-worker payment-server dev

infra-up:
	docker compose up -d
	@echo "Waiting for services to be healthy..."
	@sleep 5

infra-down:
	docker compose down -v

migrate:
	@echo "Running migrations..."
	docker exec -i fb-postgres psql -U flight_user -d flight_booking < migrations/001_initial.up.sql

seed:
	go run ./cmd/seeder/main.go

api:
	go run ./cmd/api/main.go

booking-worker:
	go run ./cmd/booking-worker/main.go

cron-worker:
	go run ./cmd/cron-worker/main.go

cdc-worker:
	go run ./cmd/cdc-worker/main.go

payment-server:
	go run ./cmd/payment-server/main.go

# Run all services in parallel
dev:
	@echo "Starting all services..."
	@make payment-server & \
	 make api & \
	 make booking-worker & \
	 make cron-worker & \
	 make cdc-worker & \
	 wait

setup: infra-up
	@sleep 5
	@make migrate
	@make seed
	@echo "✓ Infrastructure ready. Run 'make dev' to start all services."
```

---

## 13. Startup Sequence

```
Step 1:  make infra-up          # Starts Postgres, Redis, Kafka in Docker
Step 2:  make migrate           # Creates tables, indexes, enums, triggers
Step 3:  cp .env.example .env   # Configure environment (defaults work for local)
Step 4:  make seed              # Populates 60 days of flight data
Step 5:  make dev               # Starts all 5 Go processes in parallel

         ┌─────────────────┐
         │  Docker          │
         │  ┌─ Postgres     │ :5432
         │  ├─ Redis        │ :6379
         │  └─ Kafka        │ :9092
         └─────────────────┘
         ┌─────────────────┐
         │  Go Processes    │
         │  ┌─ API Server   │ :8080
         │  ├─ Payment Srv  │ :8081
         │  ├─ Booking Wkr  │ (Kafka consumer)
         │  ├─ Cron Worker  │ (ticker loops)
         │  └─ CDC Worker   │ (PG LISTEN)
         └─────────────────┘
```

---

## 14. End-to-End Test Walkthrough

Once everything is running, test the full flow with curl:

```bash
# 1. List flights
curl "http://localhost:8080/api/flights?from=DEL&to=BOM&date=2026-03-25"

# 2. Get flight details (pick an ID from step 1)
curl "http://localhost:8080/api/flights/{flightId}"

# 3. Book seats
curl -X POST http://localhost:8080/api/bookings \
  -H "Content-Type: application/json" \
  -d '{
    "flightId": "{flightId}",
    "seats": ["L1", "L2"],
    "userId": "550e8400-e29b-41d4-a716-446655440000",
    "customerDetails": {"name": "Aditya", "email": "aditya@test.com"}
  }'
# → Returns bookingIntentId, status: PENDING

# 4. Poll booking status (wait for payment callback)
curl "http://localhost:8080/api/bookings/{bookingIntentId}"
# → Eventually becomes CONFIRMED or PAYMENT_FAILED

# 5. If PAYMENT_FAILED, retry
curl -X POST "http://localhost:8080/api/bookings/{bookingIntentId}/retry"
# → Returns new paymentIntentId, status back to PENDING, expiresAt extended by 2 min

# 6. Verify seat map updated
curl "http://localhost:8080/api/flights/{flightId}"
# → L1, L2 should be in "booked" (if CONFIRMED) or "reserved" (if still PENDING)
```

---

## 15. Go Module Dependencies

```
go mod init flight-booking

go get github.com/gin-gonic/gin
go get gorm.io/gorm
go get gorm.io/driver/postgres
go get gorm.io/datatypes
go get github.com/redis/go-redis/v9
go get github.com/segmentio/kafka-go
go get github.com/jackc/pgx/v5
go get github.com/google/uuid
go get github.com/lib/pq
go get github.com/joho/godotenv
```

---

## 16. Error Handling Patterns

### 16.1 Transaction Helper

All booking-critical operations use a consistent transaction pattern:

```go
func withTx(db *gorm.DB, fn func(tx *gorm.DB) error) error {
    tx := db.Begin()
    if tx.Error != nil {
        return tx.Error
    }
    if err := fn(tx); err != nil {
        tx.Rollback()
        return err
    }
    return tx.Commit().Error
}
```

### 16.2 FOR UPDATE Helper

```go
func lockBookingIntent(tx *gorm.DB, bookingIntentID string) (*models.BookingIntent, error) {
    var bi models.BookingIntent
    err := tx.Raw(
        "SELECT * FROM booking_intents WHERE booking_intent_id = ? FOR UPDATE",
        bookingIntentID,
    ).Scan(&bi).Error
    return &bi, err
}

func lockFlight(tx *gorm.DB, flightID uuid.UUID) (*models.Flight, error) {
    var f models.Flight
    err := tx.Raw(
        "SELECT * FROM flights WHERE id = ? FOR UPDATE",
        flightID,
    ).Scan(&f).Error
    return &f, err
}
```

### 16.3 Seat Array Operations (Postgres)

```go
func addSeats(tx *gorm.DB, flightID uuid.UUID, column string, seats []string) error {
    return tx.Exec(
        fmt.Sprintf("UPDATE flights SET %s = array_cat(%s, $1) WHERE id = $2", column, column),
        pq.StringArray(seats), flightID,
    ).Error
}

func removeSeats(tx *gorm.DB, flightID uuid.UUID, column string, seats []string) error {
    return tx.Exec(
        fmt.Sprintf("UPDATE flights SET %s = (SELECT array_agg(s) FROM unnest(%s) s WHERE s != ALL($1)) WHERE id = $2", column, column),
        pq.StringArray(seats), flightID,
    ).Error
}
```

---

## 17. Redis Key Reference (Complete)

| Key Pattern | Type | TTL | Set By | Read By |
|---|---|---|---|---|
| `flights:route:{origin}:{dest}:{date}` | STRING (JSON list) | End of departure date | Flight Service (cache-aside), CDC Worker | Flight Service |
| `flights:hot:from:{origin}:{date}` | STRING (JSON list) | 5 min | Cron Worker | Flight Service |
| `flights:hot:to:{dest}:{date}` | STRING (JSON list) | 5 min | Cron Worker | Flight Service |
| `hot:sources:{date}` | SET | End of date | Cron Worker | Flight Service, CDC Worker |
| `hot:destinations:{date}` | SET | End of date | Cron Worker | Flight Service, CDC Worker |
| `metrics:search:from:{origin}:{date}` | STRING (counter) | 24h | Flight Service (INCR) | Cron Worker |
| `metrics:search:to:{dest}:{date}` | STRING (counter) | 24h | Flight Service (INCR) | Cron Worker |
| `metrics:search:route:{origin}:{dest}:{date}` | STRING (counter) | 24h | Flight Service (INCR) | Cron Worker |
