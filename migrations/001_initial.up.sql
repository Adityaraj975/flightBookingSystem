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

-- Immutable date-in-UTC for index (avoids "functions in index expression must be marked IMMUTABLE")
CREATE OR REPLACE FUNCTION flights_departure_date_utc(timestamptz) RETURNS date
AS $$ SELECT ($1 AT TIME ZONE 'UTC')::date $$ LANGUAGE sql IMMUTABLE;
CREATE INDEX idx_flights_origin_dest_date ON flights (origin, destination, flights_departure_date_utc(departure_time));
CREATE INDEX idx_flights_origin_date ON flights (origin, flights_departure_date_utc(departure_time));
CREATE INDEX idx_flights_dest_date ON flights (destination, flights_departure_date_utc(departure_time));
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
    BEFORE UPDATE ON flights FOR EACH ROW EXECUTE PROCEDURE update_updated_at();
CREATE TRIGGER trg_booking_intents_updated_at
    BEFORE UPDATE ON booking_intents FOR EACH ROW EXECUTE PROCEDURE update_updated_at();
CREATE TRIGGER trg_payment_intents_updated_at
    BEFORE UPDATE ON payment_intents FOR EACH ROW EXECUTE PROCEDURE update_updated_at();

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
    FOR EACH ROW EXECUTE PROCEDURE notify_flight_changes();
