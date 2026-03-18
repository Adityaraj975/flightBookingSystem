# Flight Booking System — High-Level Design

## 1. Overview

A flight booking system where users can browse upcoming flights, select seats, make payments, and receive booking confirmations. The system handles concurrent seat reservations, payment retries, and race conditions around payment callbacks.

### 1.1 Core Requirements

- Users can view upcoming flights with filters (date, price, source, destination).
- Users can book a flight — selecting from a fixed set of **30 seats** (rows L, M, N — 10 seats each) per flight.
- On seat selection, seats are **reserved for 10 minutes**.
- Payment is initiated; the system waits for a callback from an external payment service.
- Multiple seats can be booked in a single booking.
- If payment is not confirmed within 10 minutes, seats are released.

### 1.2 Out of Scope

- Ticket creation and communications (email/SMS).
- Real payment gateway integration (a dummy payment server simulates callbacks).

---

## 2. Architecture Overview

```
                                    ┌─────────────┐
                                    │ Cron Worker  │
                                    └──────┬───────┘
                                           │ (syncs flight data)
                                           ▼
┌────────┐     ┌──────────────┐    ┌───────────────┐     ┌────────────┐
│        │     │              │    │               │────▶│   Redis     │
│ Client │────▶│ API Gateway  │───▶│ View Flights  │     │  (cache)   │
│        │     │ + LB         │    │   Service     │◀────│            │
└────────┘     │              │    └───────────────┘     └────────────┘
               │              │                                ▲
               │              │                                │ CDC
               │              │    ┌───────────────┐     ┌────────────┐
               │              │───▶│   Booking     │────▶│ PostgreSQL │
               └──────────────┘    │   Service     │◀────│ (primary)  │
                                   └───────┬───────┘     └────────────┘
                                           │                   ▲
                                           │ (creates          │
                                           │  payment intent)  │
                                           ▼                   │
                                   ┌───────────────┐           │
                                   │ Dummy Payment │           │
                                   │   Server      │           │
                                   └───────┬───────┘           │
                                           │ (webhook callback │
                                           │  after random     │
                                           │  delay)           │
                                           ▼                   │
                                   ┌───────────────┐     ┌─────┴──────┐
                                   │   Webhook     │────▶│  Kafka     │
                                   │   Service     │     │  Queue     │
                                   └───────────────┘     └─────┬──────┘
                                                               │
                                                               ▼
                                                        ┌─────────────┐
                                                        │  Booking    │──▶ Redis
                                                        │  Worker     │──▶ PostgreSQL
                                                        └─────────────┘
```

### Component Responsibilities

| Component | Role |
|---|---|
| **API Gateway + LB** | Routes traffic, rate limiting, auto-scaling |
| **View Flights Service** | Serves flight search/listing — reads from Redis cache |
| **Booking Service** | Handles seat reservation, booking creation, payment initiation |
| **Dummy Payment Server** | Simulates external payment gateway; hits webhook after random delay with payment status |
| **Webhook Service** | Receives payment callbacks, publishes events to Kafka |
| **Booking Worker** | Consumes Kafka events, processes payment status updates, handles race conditions |
| **Cron Worker** | Pulls/generates flight data, manages booking intent cleanup (expired reservations) |
| **PostgreSQL** | Primary data store for flights, bookings, payments |
| **Redis** | Flight listing cache (hot sources/destinations), distributed locking where needed |
| **Kafka** | Async event queue between Webhook Service and Booking Worker |

---

## 3. Data Models

### 3.1 Flights Table

| Column | Type | Description |
|---|---|---|
| `id` | UUID | Primary key |
| `cargoId` | VARCHAR | External reference (airline cargo ID) |
| `origin` | VARCHAR | Source airport/city |
| `destination` | VARCHAR | Destination airport/city |
| `departureTime` | TIMESTAMP | Departure time |
| `arrivalTime` | TIMESTAMP | Arrival time |
| `price` | DECIMAL | Fare per seat |
| `status` | ENUM | `SCHEDULED`, `FULLY_BOOKED`, `DEPARTED` |
| `reservedSeats` | JSONB | Array of temporarily held seat IDs (payment in progress) |
| `bookedSeats` | JSONB | Array of permanently booked seat IDs (payment confirmed) |
| `totalSeats` | INT | Always 30 (3 rows × 10 seats) |
| `fromDestination` | VARCHAR | Indexed — for hot destination queries |
| `toDestination` | VARCHAR | Indexed — for hot source queries |
| `createdAt` | TIMESTAMP | |
| `updatedAt` | TIMESTAMP | |

> Each flight has exactly **30 seats**: rows L (L1–L10), M (M1–M10), N (N1–N10).
>
> **Available seats** = totalSeats − len(reservedSeats) − len(bookedSeats).
> When a booking is `CONFIRMED`, its seats move from `reservedSeats` → `bookedSeats`.
> When a booking `EXPIRES` or payment fails and the window runs out, seats are removed from `reservedSeats`.

**Flight.status Enum:**

| Status | Meaning |
|---|---|
| `SCHEDULED` | Upcoming flight, open for booking (available seats > 0) |
| `FULLY_BOOKED` | All 30 seats are in `bookedSeats` — no more bookings accepted |
| `DEPARTED` | Flight has passed its `departureTime` — no longer bookable |

> Transitions: `SCHEDULED` → `FULLY_BOOKED` (when last seat is confirmed) or `SCHEDULED` → `DEPARTED` (cron sets this when `departureTime < NOW()`). A `FULLY_BOOKED` flight can revert to `SCHEDULED` if a booking expires and frees seats (edge case).

### 3.2 BookingIntent Table

| Column | Type | Description |
|---|---|---|
| `id` | UUID | Primary key |
| `bookingIntentId` | VARCHAR | Unique business identifier |
| `userId` | UUID | FK → Users |
| `flightId` | UUID | FK → Flights |
| `seats` | JSONB | Array of selected seat IDs, e.g. `["L1", "M3"]` |
| `customerDetails` | JSONB | Name, email, phone, etc. |
| `activePaymentIntentId` | VARCHAR | The **currently active** PaymentIntent — only callbacks matching this ID can transition the booking |
| `status` | ENUM | `PENDING`, `PAYMENT_FAILED`, `CONFIRMED`, `EXPIRED` |
| `retryCount` | INT | Number of payment retries so far (0 on first attempt, max **3**) |
| `metadata` | JSONB | Arbitrary metadata |
| `expiresAt` | TIMESTAMP | When the seat reservation expires |
| `createdAt` | TIMESTAMP | |
| `updatedAt` | TIMESTAMP | |

### 3.3 PaymentIntent Table

| Column | Type | Description |
|---|---|---|
| `id` | UUID | Primary key |
| `paymentIntentId` | VARCHAR | Unique identifier from payment system |
| `bookingIntentId` | VARCHAR | FK → BookingIntent |
| `status` | ENUM | `INITIATED`, `SUCCESS`, `FAILED`, `EXPIRED` |
| `metadata` | JSONB | Payment details, amount, etc. |
| `createdAt` | TIMESTAMP | |
| `updatedAt` | TIMESTAMP | |

### 3.4 BookingIntentCleanup Table

| Column | Type | Description |
|---|---|---|
| `id` | UUID | Primary key |
| `bookingIntentId` | VARCHAR | FK → BookingIntent |
| `expiry` | TIMESTAMP | When this reservation should be cleaned up |

> The Cron Worker polls this table periodically to find expired reservations and release seats.

---

## 4. Status Lifecycle & State Machines

### 4.1 BookingIntent Status

```
                          
                          │                                                        
                          │  user retries payment                                  
                          │  (new PaymentIntent, extend window)                    
                          │                                                        
  ┌───────────────┐       │     payment SUCCESS         ┌─────────────┐            
  │               │───────┼───────────────────────────▶ │  CONFIRMED  │            
  │   PENDING     │       │                             └─────────────┘            
  │               │       │     payment FAILED          ┌────────────────┐         
  │  (seats held, │───────┼───────────────────────────▶ │ PAYMENT_FAILED │
  │  waiting for  │       │                             │                │
  │  callback)    │       │                             │  (seats still  │
  │               │       │                             │   reserved,    │
  └───────┬───────┘       │                             │   can retry)   │
          │               │                             └───────┬────────┘
          │               │                                     │
          │  reservation  │                                     │ reservation
          │  expired      │                                     │ expired
          │               │                                     │
          ▼               │                                     ▼
  ┌───────────────┐       │                             ┌───────────────┐
  │    EXPIRED    │       │                             │    EXPIRED    │
  │               │       │                             │               │
  │ (seats freed) │       │                             │ (seats freed) │
  └───────────────┘       │                             └───────────────┘
```

**State Definitions:**

| Status | Terminal? | Meaning |
|---|---|---|
| `PENDING` | No | Seats reserved, payment initiated, waiting for callback |
| `PAYMENT_FAILED` | **No** | Latest payment attempt was declined; seats are **still reserved**; user can retry within the reservation window |
| `CONFIRMED` | Yes | Payment succeeded, seats moved from `reservedSeats` → `bookedSeats`, booking is final |
| `EXPIRED` | Yes | Reservation window elapsed without successful payment, seats removed from `reservedSeats` |

**Why `PAYMENT_FAILED` is non-terminal:** With `PAYMENT_FAILED`, seats stay reserved while the user decides whether to retry, until the reservation window expires.

### 4.2 PaymentIntent Status

```
  INITIATED ──────▶ SUCCESS
      │
      ├──────────▶ FAILED
      │
      └──────────▶ EXPIRED
```

| Status | Meaning |
|---|---|
| `INITIATED` | Payment request sent to payment server, awaiting callback |
| `SUCCESS` | Payment confirmed by payment provider |
| `FAILED` | Payment explicitly declined by payment provider |
| `EXPIRED` | No callback received; marked by Cron Worker during booking cleanup |

> `INITIATED` (not `PENDING`) — clearer that the request has been dispatched, distinguishes from "not yet sent."

### 4.3 Critical Status Change Rules

1. **`PENDING` → `CONFIRMED`**: Only on receiving a `SUCCESS` callback for the **current** `activePaymentIntentId`. Seats move from `reservedSeats` → `bookedSeats` on the flight. Cleanup row is deleted. All other `INITIATED` PaymentIntents for this booking are marked `EXPIRED`.
2. **`PENDING` → `PAYMENT_FAILED`**: On receiving a `FAILED` callback for the current `activePaymentIntentId`. **Seats remain in `reservedSeats`** — the user still has time to retry.
3. **`PAYMENT_FAILED` → `PENDING`**: User initiates a retry. New `PaymentIntent` is created (`INITIATED`), `activePaymentIntentId` is updated on the BookingIntent, `retryCount` is incremented, `expiresAt` is extended by **+2 minutes from now**. Retry is only allowed if `retryCount < 3`.
4. **`PENDING` → `EXPIRED`**: Cron Worker finds an expired `BookingIntentCleanup` entry. Seats are removed from `reservedSeats`. All `INITIATED` PaymentIntents for this booking are marked `EXPIRED`.
5. **`PAYMENT_FAILED` → `EXPIRED`**: Same as above — user didn't retry before the window elapsed.
6. **Once `CONFIRMED` or `EXPIRED` — no further state transitions are allowed.** These are the only two terminal states. This is critical for race condition safety.

**Retry Constraints:**

| Parameter | Value |
|---|---|
| Initial reservation window | 10 minutes |
| Extension per retry | +2 minutes (from the time of retry) |
| Max retries | 3 |
| Max possible window | ~16 minutes (10 + 2 + 2 + 2, if all retries are at the last second) |

> The extension is deliberately small (+2 min, not +10 min) to prevent users from holding seats indefinitely through repeated retries. After 3 failed attempts, the user must wait for the booking to expire and start fresh.

### 4.4 State Transition Summary

```
BookingIntent:
  PENDING         → CONFIRMED        (payment SUCCESS)
  PENDING         → PAYMENT_FAILED   (payment FAILED)
  PENDING         → EXPIRED          (cron cleanup)
  PAYMENT_FAILED  → PENDING          (user retries)
  PAYMENT_FAILED  → EXPIRED          (cron cleanup)

PaymentIntent:
  INITIATED  → SUCCESS   (callback received, payment confirmed)
  INITIATED  → FAILED    (callback received, payment declined)
  INITIATED  → EXPIRED   (no callback, booking expired by cron)

Flight.status:
  SCHEDULED    → FULLY_BOOKED  (last available seat confirmed)
  SCHEDULED    → DEPARTED      (departureTime passed, set by cron)
  FULLY_BOOKED → SCHEDULED     (booking expired, seats freed)
  FULLY_BOOKED → DEPARTED      (departureTime passed)
```

### 4.5 Callback Processing Algorithm (Booking Worker)

A BookingIntent can have **multiple PaymentIntents** over its lifetime (one per attempt). Only the `activePaymentIntentId` on the BookingIntent represents the current attempt. When a payment callback arrives, the Booking Worker must decide: book, ignore, or refund.

**Lookup:** The worker receives `{ paymentIntentId, status }` from Kafka. It looks up the PaymentIntent row to find the `bookingIntentId`, then locks the BookingIntent `FOR UPDATE`.

**Decision Tree:**

```
callback arrives for paymentIntentId = pi_X, status = S
│
├─ 1. Look up PaymentIntent row for pi_X → get bookingIntentId
├─ 2. SELECT * FROM booking_intents WHERE bookingIntentId = ? FOR UPDATE
│
├─ Is BookingIntent in a terminal state (CONFIRMED or EXPIRED)?
│   │
│   ├─ YES, CONFIRMED:
│   │   ├─ callback S = SUCCESS → pi_X is a stale payment that also succeeded
│   │   │   → Mark pi_X PaymentIntent as SUCCESS
│   │   │   → **TRIGGER REFUND** for pi_X (booking already confirmed by another PI)
│   │   │
│   │   └─ callback S = FAILED → pi_X payment failed, booking already confirmed
│   │       → Mark pi_X PaymentIntent as FAILED (audit only, no action needed)
│   │
│   └─ YES, EXPIRED:
│       ├─ callback S = SUCCESS → payment succeeded but booking already expired
│       │   → Mark pi_X PaymentIntent as SUCCESS
│       │   → **TRIGGER REFUND** for pi_X (seats already released)
│       │
│       └─ callback S = FAILED → payment failed, booking already expired
│           → Mark pi_X PaymentIntent as FAILED (audit only)
│
├─ Is BookingIntent non-terminal (PENDING or PAYMENT_FAILED)?
│   │
│   ├─ Does pi_X match activePaymentIntentId?
│   │   │
│   │   ├─ YES (this is the current active payment):
│   │   │   ├─ S = SUCCESS:
│   │   │   │   → UPDATE BookingIntent status = CONFIRMED
│   │   │   │   → UPDATE pi_X PaymentIntent status = SUCCESS
│   │   │   │   → Move seats: reservedSeats → bookedSeats
│   │   │   │   → Delete BookingIntentCleanup row
│   │   │   │   → Mark ALL other INITIATED PaymentIntents for this
│   │   │   │     bookingIntentId as EXPIRED (they're now irrelevant)
│   │   │   │   → **Book on first success — done.**
│   │   │   │
│   │   │   └─ S = FAILED:
│   │   │       → UPDATE BookingIntent status = PAYMENT_FAILED
│   │   │       → UPDATE pi_X PaymentIntent status = FAILED
│   │   │       → Seats stay in reservedSeats (user can retry)
│   │   │
│   │   └─ NO (this is a stale/old PaymentIntent):
│   │       ├─ S = SUCCESS:
│   │       │   → Mark pi_X PaymentIntent as SUCCESS
│   │       │   → **TRIGGER REFUND** for pi_X
│   │       │   → DO NOT change BookingIntent status
│   │       │     (the active PI is still in-flight; let it resolve)
│   │       │
│   │       └─ S = FAILED:
│   │           → Mark pi_X PaymentIntent as FAILED (audit only)
│   │           → No BookingIntent change
│   │
│   └─ COMMIT transaction
```

**Key Principle — "Book on first success, refund the rest":**

A BookingIntent with 3 retries could have 4 PaymentIntents (pi_1, pi_2, pi_3, pi_4). Callbacks arrive in unpredictable order. The rule is simple:

- The **first SUCCESS callback that matches `activePaymentIntentId`** wins → booking is CONFIRMED.
- Any other SUCCESS callback (stale PI, or callback arriving after CONFIRMED/EXPIRED) → **automatic refund**.
- FAILED callbacks for stale PIs → audit log only, no state change.

**Refund Trigger:**

The refund is handled asynchronously. When the worker identifies a refund-eligible callback:
1. Mark the PaymentIntent status = `SUCCESS` (it did succeed at the payment provider).
2. Insert a row into a `RefundQueue` table (or publish to a `refunds` Kafka topic).
3. The refund is processed out-of-band (out of scope for this system, but the trigger point is well-defined).

---

## 5. Flight View & Redis Caching Strategy

### 5.1 Flight Data Ingestion

The **Cron Worker** runs periodically and populates the system with flight data:

- Date range: next 60 days from today.
- Filters by configurable thresholds: `#sourceThreshold`, `#destinationThreshold`, `#dates`.
- For each date, the worker stores flight data into both PostgreSQL (source of truth) and Redis (cache for fast reads).

### 5.2 Redis Key Design — Flight Cache

The View Flights service reads from Redis for low-latency search. Keys are organized around the primary query pattern: "flights from X to Y on date Z".

**Route-level Keys (cache-aside, populated on demand):**

```
flights:route:{origin}:{destination}:{date}  →  List<FlightJSON>
```

Example:
```
flights:route:DEL:BOM:2026-03-20  →  [{ flightId, price, departureTime, ... }, ...]
flights:route:BOM:BLR:2026-03-21  →  [{ flightId, price, departureTime, ... }, ...]
```

These are populated on cache miss — when a user searches a route that isn't in Redis, the View Flights service queries Postgres, writes the result to Redis, and returns it. TTL = end of the departure date.

**Hot Source Keys (pre-warmed, all flights from a popular origin):**

```
flights:hot:from:{origin}:{date}  →  List<FlightJSON>
```

**Hot Destination Keys (pre-warmed, all flights to a popular destination):**

```
flights:hot:to:{destination}:{date}  →  List<FlightJSON>
```

Hot keys are **not** populated on demand. They are pre-warmed by the Cron Worker based on metrics (see 5.3). This means a search for "flights from DEL on March 20" can be served entirely from the hot key without hitting Postgres, even if the specific route (DEL→BOM, DEL→GOI, etc.) hasn't been searched before.

**TTL Policy:**
- All flight keys expire at **end of the flight's departure date** (no stale flights).
- Hot keys are refreshed by the Cron Worker every run (typically every 5 minutes).

**CDC Sync (Postgres → Redis):**
- When a booking changes seat availability in Postgres, a CDC event (e.g., Debezium) captures the row change and updates the corresponding Redis keys (both route-level and hot keys).
- This ensures the View Flights service reflects near-real-time seat availability without direct coupling to the Booking Service.

### 5.3 Hot Key Identification — Metrics & Strategy

"Hot" sources and destinations are not hardcoded — they are **dynamically identified** from real user search traffic. The system tracks search frequency and uses thresholds to decide which origins/destinations deserve pre-warmed cache keys.

**Step 1: Track search metrics in Redis**

Every time a user searches for flights, the View Flights service increments counters:

```
INCR  metrics:search:from:{origin}:{date}
INCR  metrics:search:to:{destination}:{date}
INCR  metrics:search:route:{origin}:{destination}:{date}
```

These counters use a **sliding window** — each key has a TTL of **24 hours**, so they naturally decay. At any point, the counter value represents "searches in the last 24 hours."

Example: when a user searches DEL → BOM on 2026-03-20:
```
INCR metrics:search:from:DEL:2026-03-20       → 4521
INCR metrics:search:to:BOM:2026-03-20         → 3892
INCR metrics:search:route:DEL:BOM:2026-03-20  → 1203
```

**Step 2: Cron Worker evaluates thresholds**

The Cron Worker runs every **5 minutes** and scans the metric counters to determine which origins/destinations are "hot":

```
For each date in (today ... today + 60 days):
  For each origin with a metrics:search:from:{origin}:{date} key:
    count = GET metrics:search:from:{origin}:{date}
    IF count >= SOURCE_THRESHOLD:
      Mark origin as hot for that date
      → Query Postgres for ALL flights from {origin} on {date}
      → Write to flights:hot:from:{origin}:{date}

  For each destination with a metrics:search:to:{destination}:{date} key:
    count = GET metrics:search:to:{destination}:{date}
    IF count >= DESTINATION_THRESHOLD:
      Mark destination as hot for that date
      → Query Postgres for ALL flights to {destination} on {date}
      → Write to flights:hot:to:{destination}:{date}
```

**Step 3: Configuration — thresholds**

| Parameter | Default | Description |
|---|---|---|
| `SOURCE_THRESHOLD` | 500 searches/24h | Minimum search count for an origin to be considered "hot" |
| `DESTINATION_THRESHOLD` | 500 searches/24h | Minimum search count for a destination to be considered "hot" |
| `ROUTE_THRESHOLD` | 200 searches/24h | Minimum for a specific route to get pre-warmed (optional, finer grain) |
| `HOT_KEY_TTL` | 5 minutes | How long a hot key lives before the Cron Worker must refresh it |
| `METRIC_TTL` | 24 hours | Sliding window for search counters |

> Thresholds are tunable. Start conservative (500), lower if cache hit rate is poor, raise if Redis memory is a concern.

**Step 4: Tracking hot key membership**

The Cron Worker maintains a set of currently-hot origins and destinations so it knows what to refresh:

```
SADD  hot:sources:{date}       DEL BOM BLR HYD
SADD  hot:destinations:{date}  GOA BOM DEL CCU
```

On each run, it compares the current metric counts against thresholds. If an origin drops below the threshold, it is removed from the set and its hot key is allowed to expire naturally (TTL).

**Why this works:**
- **No hardcoding** — hot routes change dynamically (e.g., GOA becomes hot during holiday season, drops off after).
- **Low overhead** — `INCR` is O(1), counter keys auto-expire, no manual cleanup.
- **Graceful degradation** — if the Cron Worker is delayed, hot keys expire (5-min TTL) and the View Flights service falls back to cache-aside (route-level key or Postgres query). No stale data.

### 5.4 Query Flow

```
Client → API Gateway → View Flights Service
                              │
                              ├─ Is origin hot? Check SET hot:sources:{date}
                              │     ├─ YES → Read flights:hot:from:{origin}:{date}
                              │     │         Filter to matching destination in-memory
                              │     │
                              │     └─ NO → Check flights:route:{origin}:{dest}:{date}
                              │               ├─ HIT → return cached flights
                              │               └─ MISS → query Postgres → populate cache → return
                              │
                              ├─ INCR metrics:search:from:{origin}:{date}
                              ├─ INCR metrics:search:to:{dest}:{date}
                              └─ INCR metrics:search:route:{origin}:{dest}:{date}
```

> The metric increment happens on every search regardless of cache hit/miss — it measures user demand, not cache performance.

---

## 6. Booking Flow — Happy Case Simulation

This section walks through the complete booking lifecycle step by step.

### 6.1 Step-by-Step: Happy Path

```
 Time   Actor               Action
─────   ─────               ──────
 T+0    User                Selects flight F1, seats [L1, L2]
 T+0    Booking Service     Validates L1, L2 not in reservedSeats OR bookedSeats on F1
 T+0    Booking Service     BEGIN transaction:
                              - SELECT * FROM flights WHERE id = F1 FOR UPDATE
                              - Check: L1, L2 not in reservedSeats ∪ bookedSeats
                              - UPDATE flights SET reservedSeats = reservedSeats || '["L1","L2"]'
                              - INSERT BookingIntent (status=PENDING, expiresAt=T+10min)
                              - INSERT PaymentIntent (status=INITIATED)
                              - INSERT BookingIntentCleanup (expiry=T+10min)
                            COMMIT
 T+0    Booking Service     Calls Dummy Payment Server with paymentIntentId + amount
 T+0    Dummy Payment       Acknowledges receipt, will callback after random delay (1–8 min)
 T+0    Booking Service     Returns bookingIntentId to user (status: PENDING)

 T+3m   Dummy Payment       Sends POST webhook → Webhook Service
                              { paymentIntentId: "pi_123", status: "SUCCESS" }
 T+3m   Webhook Service     Publishes event to Kafka topic: `payment.callbacks`
 T+3m   Booking Worker      Consumes event from Kafka
 T+3m   Booking Worker      BEGIN transaction:
                              - Look up PaymentIntent pi_123 → bookingIntentId = 'bi_456'
                              - SELECT * FROM booking_intents
                                WHERE bookingIntentId = 'bi_456'
                                FOR UPDATE                         -- row lock
                              - Verify status is PENDING
                              - Verify activePaymentIntentId matches 'pi_123'
                              - UPDATE booking_intents SET status = 'CONFIRMED'
                              - UPDATE payment_intents SET status = 'SUCCESS'
                              - UPDATE flights SET
                                  reservedSeats = reservedSeats - '["L1","L2"]',
                                  bookedSeats   = bookedSeats   || '["L1","L2"]'
                              - DELETE FROM booking_intent_cleanup
                                WHERE bookingIntentId = 'bi_456'
                              - IF len(bookedSeats) = 30 → UPDATE flights SET status = 'FULLY_BOOKED'
                            COMMIT
 T+3m   Booking Worker      Seats L1, L2 are now permanently booked on F1
```

### 6.2 Step-by-Step: Payment Failed → Retry → Success

```
 Time    Actor               Action
──────   ─────               ──────
 T+0     User                Books seats [M5], BookingIntent created (PENDING)
                             expiresAt = T+10m, retryCount = 0
 T+0     Booking Service     Calls Dummy Payment Server → pi_1 (activePaymentIntentId)

 T+2m    Dummy Payment       Sends callback for pi_1 → status: FAILED
 T+2m    Booking Worker      BEGIN transaction:
                               SELECT ... FOR UPDATE on BookingIntent
                               status = PENDING, activePaymentIntentId = pi_1 ✓ (matches)
                               → UPDATE BookingIntent SET status = 'PAYMENT_FAILED'
                               → UPDATE PaymentIntent pi_1 SET status = 'FAILED'
                             COMMIT
                             Seats [M5] remain in reservedSeats (NOT released)
                             User can still retry (retryCount 0 < max 3).

 T+4m    Client              Polls GET /bookings/:id
                             → { status: PAYMENT_FAILED, retryCount: 0, maxRetries: 3 }
                             UI shows: "Payment failed. Retry? (2 of 3 retries remaining)"
 T+4m    User                Clicks retry
 T+4m    Booking Service     BEGIN transaction:
                               SELECT ... FOR UPDATE on BookingIntent
                               status = PAYMENT_FAILED ✓, retryCount = 0 < 3 ✓
                               - INSERT new PaymentIntent (pi_2, status=INITIATED)
                               - UPDATE BookingIntent SET
                                   activePaymentIntentId = pi_2,
                                   retryCount = 1,
                                   status = 'PENDING',
                                   expiresAt = T+6min (now + 2 min)
                               - UPDATE BookingIntentCleanup SET expiry = T+6min
                             COMMIT
                             Calls Dummy Payment Server with pi_2

 T+5m    Dummy Payment       Sends callback for pi_2 → status: SUCCESS
 T+5m    Booking Worker      SELECT ... FOR UPDATE on BookingIntent
                             status = PENDING, activePaymentIntentId = pi_2 ✓
                             → UPDATE status = 'CONFIRMED'
                             → Mark pi_1 (FAILED) — already terminal, no change needed
                             → Move M5 from reservedSeats → bookedSeats
                             → Delete BookingIntentCleanup row
                             Seats permanently booked.
```

### 6.2b Step-by-Step: Multiple Retries, Stale SUCCESS → Refund

```
 Time    Actor               Action
──────   ─────               ──────
 T+0     User                Books seats [M5], BookingIntent = PENDING, pi_1
                             expiresAt = T+10m, retryCount = 0

 T+5m    Client              No callback yet, user opts to retry
 T+5m    Booking Service     retryCount = 0 < 3 ✓
                             INSERT pi_2 (INITIATED), activePaymentIntentId = pi_2
                             retryCount = 1, expiresAt = T+7m (now + 2 min)
                             Calls Dummy Payment Server with pi_2

 T+6m    Dummy Payment       Late callback for pi_1 → SUCCESS
 T+6m    Booking Worker      activePaymentIntentId = pi_2 ≠ pi_1
                             ⚠️  Stale SUCCESS → mark pi_1 as SUCCESS
                             → **TRIGGER REFUND for pi_1**
                             → No BookingIntent change (pi_2 is still in-flight)

 T+6.5m  Dummy Payment       Callback for pi_2 → FAILED
 T+6.5m  Booking Worker      activePaymentIntentId = pi_2 ✓
                             → UPDATE BookingIntent status = PAYMENT_FAILED

 T+6.8m  User                Retries again (retryCount 1 < 3 ✓)
 T+6.8m  Booking Service     INSERT pi_3 (INITIATED), activePaymentIntentId = pi_3
                             retryCount = 2, expiresAt = T+8.8m (now + 2 min)
                             Calls Dummy Payment Server with pi_3

 T+7.5m  Dummy Payment       Callback for pi_3 → SUCCESS
 T+7.5m  Booking Worker      activePaymentIntentId = pi_3 ✓, status = PENDING ✓
                             → CONFIRMED. Move seats reserved → booked.

 Summary: pi_1 = SUCCESS (refunded), pi_2 = FAILED, pi_3 = SUCCESS (booked)
```

### 6.2c Step-by-Step: Max Retries Exhausted → Expired

```
 Time    Actor               Action
──────   ─────               ──────
 T+0     User                Books [M5], pi_1, expiresAt = T+10m, retryCount = 0
 T+2m    pi_1 callback       FAILED → status = PAYMENT_FAILED
 T+3m    User retries        pi_2, retryCount = 1, expiresAt = T+5m
 T+4m    pi_2 callback       FAILED → status = PAYMENT_FAILED
 T+4.5m  User retries        pi_3, retryCount = 2, expiresAt = T+6.5m
 T+5.5m  pi_3 callback       FAILED → status = PAYMENT_FAILED
 T+6m    User retries        pi_4, retryCount = 3, expiresAt = T+8m
 T+7m    pi_4 callback       FAILED → status = PAYMENT_FAILED

 T+7.5m  User tries retry    retryCount = 3 = max → REJECTED by Booking Service
                             Error: "Maximum payment retries exhausted.
                                     Wait for booking to expire or seats to be released."

 T+8m    Cron Worker         BookingIntentCleanup expiry reached
                             → EXPIRED, seats freed
```

### 6.3 Step-by-Step: Expiry & Cleanup (with late SUCCESS refund)

```
 Time    Actor               Action
──────   ─────               ──────
 T+0     User                Books seats [N1, N2], BookingIntent = PENDING
 T+0     Booking Service     Calls Dummy Payment Server (pi_1)
         ... no callback, user doesn't retry ...

 T+10m   Cron Worker         Scans BookingIntentCleanup WHERE expiry <= NOW()
                             Finds bookingIntentId = 'bi_999' (cleanup.expiry = T+10m)
 T+10m   Cron Worker         BEGIN transaction:
                               - SELECT * FROM booking_intents
                                 WHERE bookingIntentId = 'bi_999'
                                 FOR UPDATE
                               - status = PENDING (non-terminal) ✓
                               - Check expiresAt: T+10m <= NOW() ✓ (truly expired, no retry extended it)
                               - UPDATE booking_intents SET status = 'EXPIRED'
                               - UPDATE payment_intents SET status = 'EXPIRED'
                                 WHERE bookingIntentId = 'bi_999' AND status = 'INITIATED'
                               - UPDATE flights
                                 SET reservedSeats = reservedSeats - '["N1","N2"]'
                                 WHERE id = F1
                               - DELETE FROM booking_intent_cleanup
                                 WHERE bookingIntentId = 'bi_999'
                             COMMIT
 T+10m                       Seats N1, N2 are now free for other users.

 T+12m   Dummy Payment       Late callback arrives for pi_1 → status: SUCCESS
 T+12m   Booking Worker      SELECT ... FOR UPDATE on BookingIntent
                             Status = EXPIRED (terminal)
                             Callback is SUCCESS → money was charged but booking is dead
                             → Mark pi_1 PaymentIntent as SUCCESS (it did succeed)
                             → **TRIGGER REFUND for pi_1**
                             Seats remain free. Refund is processed out-of-band.
```

### 6.4 Step-by-Step: Cron Picks Up Stale Cleanup Row (Retry Extended Expiry)

```
 Time    Actor               Action
──────   ─────               ──────
 T+0     User                Books seats [L5], BookingIntent = PENDING
                             expiresAt = T+10m, cleanup.expiry = T+10m

 T+8m    pi_1 callback       FAILED → status = PAYMENT_FAILED
 T+9m    User retries        pi_2 created, expiresAt = T+11m, retryCount = 1
                             BookingIntent.expiresAt updated to T+11m
                             BookingIntentCleanup.expiry updated to T+11m

 T+10m   Cron Worker         Scans BookingIntentCleanup WHERE expiry <= NOW()
                             ⚠️  Due to timing, suppose the cron SELECT ran just before
                             the retry updated the cleanup row, or a concurrent read
                             picked up the row at the old expiry. It finds bi_xxx.

 T+10m   Cron Worker         BEGIN transaction:
                               SELECT * FROM booking_intents
                                 WHERE bookingIntentId = 'bi_xxx' FOR UPDATE
                               status = PENDING (non-terminal)
                               Check expiresAt: T+11m > NOW() (T+10m)
                               ⚠️  NOT truly expired — retry extended the window!
                               → UPDATE booking_intent_cleanup SET expiry = T+11m
                             COMMIT (booking untouched, cleanup row corrected)

 T+11m   Cron Worker         Next scan picks up bi_xxx again (expiry = T+11m <= NOW())
                             BEGIN transaction:
                               FOR UPDATE on BookingIntent
                               expiresAt = T+11m <= NOW() ✓ (truly expired now)
                               → EXPIRED, seats freed
```

> The double-check on `expiresAt` inside the `FOR UPDATE` lock is what prevents false expiry. The cleanup table is just an index for efficient scanning — the BookingIntent's `expiresAt` is always the source of truth.

---

## 7. Race Conditions & Mitigation

### 7.1 Race Condition #1: Two Users Booking the Same Seat

**Scenario:** User A and User B both try to book seat L1 on Flight F1 at nearly the same time.

```
 Time    User A (Thread 1)                    User B (Thread 2)
──────   ──────────────────                   ──────────────────
 T+0     BEGIN transaction                    BEGIN transaction
 T+0     SELECT * FROM flights                SELECT * FROM flights
         WHERE id = F1                        WHERE id = F1
         FOR UPDATE         ← acquires lock
                                              ← BLOCKED (waiting for lock)
 T+0     Check: L1 not in
         reservedSeats ∪ bookedSeats ✓
 T+0     UPDATE flights SET
         reservedSeats = reservedSeats || '["L1"]'
 T+0     INSERT BookingIntent(A, status=PENDING)
 T+0     COMMIT             ← releases lock
                                              ← acquires lock
                                              Check: L1 in reservedSeats ✗
                                              ROLLBACK → return error
                                              "Seat L1 is no longer available"
```

**Mitigation:** `SELECT ... FOR UPDATE` on the flights row serializes concurrent seat reservations. The second transaction sees the updated `reservedSeats` and fails gracefully. The check covers both `reservedSeats` (temporary holds) and `bookedSeats` (confirmed bookings).

### 7.2 Race Condition #2: Duplicate Payment Callbacks

**Scenario:** The Dummy Payment Server sends the same SUCCESS callback twice (network retry, duplicate webhook delivery).

```
 Time    Callback 1 (Worker Thread A)         Callback 2 (Worker Thread B)
──────   ────────────────────────────         ────────────────────────────
 T+0     Consume from Kafka                   Consume from Kafka (duplicate)
 T+0     BEGIN transaction                    BEGIN transaction
 T+0     SELECT * FROM booking_intents        SELECT * FROM booking_intents
         WHERE bookingIntentId = 'bi_123'     WHERE bookingIntentId = 'bi_123'
         FOR UPDATE         ← acquires lock
                                              ← BLOCKED
 T+0     Status = PENDING ✓
         activePaymentIntentId matches ✓
         UPDATE SET status = CONFIRMED
         Move seats: reserved → booked
         COMMIT             ← releases lock
                                              ← acquires lock
                                              Status = CONFIRMED (terminal)
                                              Same PI → duplicate, no-op.
                                              COMMIT (no changes)
```

**Mitigation:** The `FOR UPDATE` lock + terminal status check ensures idempotent processing. Only the first callback transitions the state.

### 7.3 Race Condition #3: Payment Callback vs. Expiry Cleanup

**Scenario:** A payment SUCCESS callback arrives at almost the exact same time the Cron Worker tries to expire the booking.

```
 Time    Booking Worker (callback)            Cron Worker (cleanup)
──────   ────────────────────────             ────────────────────
 T+10m   Consume SUCCESS from Kafka           Scan: expiry <= NOW()
         BEGIN transaction                    BEGIN transaction
         SELECT * FROM booking_intents        SELECT * FROM booking_intents
         WHERE bi = 'bi_456'                  WHERE bi = 'bi_456'
         FOR UPDATE         ← acquires lock
                                              ← BLOCKED
         Status = PENDING ✓
         activePaymentIntentId matches ✓
         UPDATE SET status = CONFIRMED
         Move seats: reserved → booked
         DELETE cleanup row
         COMMIT             ← releases lock
                                              ← acquires lock
                                              Status = CONFIRMED (terminal)
                                              ⚠️  Skip — already confirmed
                                              COMMIT (no changes)
```

**Mitigation:** Same pattern — `FOR UPDATE` serializes the two competing operations. Whichever acquires the lock first wins. The loser sees a terminal state and does nothing.

> **Note:** If the Cron Worker wins the lock first, the booking becomes `EXPIRED` and the late SUCCESS callback is ignored. The user's payment succeeded but the booking expired — this triggers an out-of-band refund flow.

### 7.4 Race Condition #4: Stale Payment Callback After Retry — Refund

**Scenario:** User retries payment (new `activePaymentIntentId`), then the OLD payment callback arrives with SUCCESS. Money was charged for the old payment — it must be refunded.

```
 Time    Event
──────   ─────
 T+0     BookingIntent created: activePaymentIntentId = pi_OLD
 T+5m    User retries → activePaymentIntentId updated to pi_NEW, expiresAt extended +2min
 T+7m    Callback for pi_OLD arrives (SUCCESS)
         Booking Worker:
           SELECT ... FOR UPDATE on BookingIntent
           activePaymentIntentId = pi_NEW ≠ pi_OLD
           ⚠️  Stale SUCCESS — money was charged but this isn't the active payment
           → Mark pi_OLD PaymentIntent as SUCCESS
           → **TRIGGER REFUND for pi_OLD**
           → No BookingIntent state change (pi_NEW is still in-flight)
 T+9m    Callback for pi_NEW arrives (SUCCESS)
           activePaymentIntentId = pi_NEW ✓, status = PENDING ✓
           → CONFIRMED, seats moved reserved → booked
```

**Mitigation:** The Booking Worker validates that the callback's `paymentIntentId` matches `activePaymentIntentId`. Stale SUCCESS callbacks are recorded and trigger an automatic refund. The booking itself is only confirmed by the active payment.

### 7.5 Race Condition #5: Two SUCCESS Callbacks for Same BookingIntent (Different PaymentIntents)

**Scenario:** Both pi_OLD and pi_NEW get SUCCESS callbacks arriving nearly simultaneously. Only one should book; the other must be refunded.

**Case A: pi_OLD processes first (stale wins the lock)**

```
 Time    Thread A (pi_OLD SUCCESS)            Thread B (pi_NEW SUCCESS)
──────   ──────────────────────────           ──────────────────────────
 T+0     BEGIN transaction                    BEGIN transaction
         SELECT ... FOR UPDATE  ← lock
                                              ← BLOCKED
         activePaymentIntentId = pi_NEW ≠ pi_OLD
         → Stale SUCCESS
         → Mark pi_OLD as SUCCESS
         → TRIGGER REFUND for pi_OLD
         COMMIT              ← releases lock
                                              ← acquires lock
                                              activePaymentIntentId = pi_NEW ✓
                                              status = PENDING ✓
                                              → CONFIRMED, seats reserved → booked
                                              COMMIT
```

Result: pi_NEW books, pi_OLD refunded. Correct.

**Case B: pi_NEW processes first (active wins the lock)**

```
 Time    Thread A (pi_NEW SUCCESS)            Thread B (pi_OLD SUCCESS)
──────   ──────────────────────────           ──────────────────────────
 T+0     BEGIN transaction                    BEGIN transaction
         SELECT ... FOR UPDATE  ← lock
                                              ← BLOCKED
         activePaymentIntentId = pi_NEW ✓
         status = PENDING ✓
         → CONFIRMED, seats reserved → booked
         → Mark all other INITIATED PIs as EXPIRED
         COMMIT              ← releases lock
                                              ← acquires lock
                                              status = CONFIRMED (terminal)
                                              pi_OLD is SUCCESS → booking already confirmed
                                              → Mark pi_OLD as SUCCESS
                                              → TRIGGER REFUND for pi_OLD
                                              COMMIT
```

Result: pi_NEW books, pi_OLD refunded. Same correct outcome regardless of lock order.

**Mitigation:** The algorithm in §4.5 guarantees exactly one booking and refund for every other SUCCESS, no matter which thread wins the lock.

### 7.6 Race Condition #6: Retry Request vs. Incoming Callback

**Scenario:** User clicks "Retry" at the exact moment a FAILED callback arrives for the current payment.

```
 Time    Booking Service (retry request)      Booking Worker (FAILED callback for pi_OLD)
──────   ──────────────────────────────       ──────────────────────────────────────────
 T+0     BEGIN transaction                    BEGIN transaction
         SELECT ... FOR UPDATE  ← lock
                                              ← BLOCKED
         status = PENDING ✓
         retryCount = 1 < 3 ✓
         Create new PaymentIntent pi_NEW
         UPDATE BookingIntent:
           activePaymentIntentId = pi_NEW
           status = PENDING (stays)
           retryCount = 2
           expiresAt += 2 min
         COMMIT              ← releases lock
                                              ← acquires lock
                                              activePaymentIntentId = pi_NEW ≠ pi_OLD
                                              ⚠️  Stale FAILED → mark pi_OLD as FAILED
                                              COMMIT (no BookingIntent change)
```

**Mitigation:** The retry acquires the lock first, updates `activePaymentIntentId`. When the FAILED callback gets the lock, it sees a mismatch and becomes a no-op on the BookingIntent. If the stale callback had been SUCCESS instead, it would trigger a refund (same pattern as §7.4).

### 7.7 Race Condition #7: Booking Confirmed, Then Another PI Succeeds

**Scenario:** BookingIntent has pi_1 and pi_2. pi_2 (active) succeeds and confirms the booking. Later, pi_1 also returns SUCCESS.

```
 Time    Event
──────   ─────
 T+0     BookingIntent created with pi_1
 T+3m    User retries → activePaymentIntentId = pi_2
 T+5m    pi_2 callback SUCCESS → Booking Worker confirms booking
         status = CONFIRMED, seats booked, cleanup row deleted

 T+8m    pi_1 callback SUCCESS (very late)
         Booking Worker:
           SELECT ... FOR UPDATE on BookingIntent
           status = CONFIRMED (terminal)
           callback is SUCCESS for pi_1 (not the active PI, but doesn't matter — it's terminal)
           → Mark pi_1 PaymentIntent as SUCCESS
           → **TRIGGER REFUND for pi_1**
           → No BookingIntent change
```

**Mitigation:** Any SUCCESS callback that arrives after CONFIRMED (from a different PI) always triggers a refund. The decision tree in §4.5 handles this — terminal state + SUCCESS = refund.

---

## 8. Dummy Payment Server (Webhook Simulation)

Since real payment gateway integration is out of scope, the system uses a dummy payment server that simulates asynchronous payment processing.

### 8.1 Behavior

```
Booking Service                         Dummy Payment Server
     │                                         │
     │  POST /create-payment                   │
     │  { paymentIntentId, amount,             │
     │    callbackUrl }                        │
     │────────────────────────────────────────▶│
     │                                         │
     │  200 OK { status: "ACKNOWLEDGED" }      │
     │◀────────────────────────────────────────│
     │                                         │
     │         ... random delay (1-8 min) ...  │
     │                                         │
     │                    POST {callbackUrl}   │
     │                    { paymentIntentId,   │
     │                      status: SUCCESS    │
     │                      | FAILED }         │
     │◀────────────────────────────────────────│
     │                                         │
```

### 8.2 Dummy Server Specification

**Endpoint: `POST /create-payment`**

Request:
```json
{
  "paymentIntentId": "pi_abc123",
  "amount": 4500.00,
  "currency": "INR",
  "callbackUrl": "https://our-api.com/webhooks/payment"
}
```

Response (immediate):
```json
{
  "status": "ACKNOWLEDGED",
  "paymentIntentId": "pi_abc123"
}
```

**Callback behavior (async, after random delay):**
- Delay: random between **1–8 minutes**.
- Outcome: weighted random — ~80% SUCCESS, ~20% FAILED (configurable).
- Sends `POST` to the `callbackUrl`:

```json
{
  "paymentIntentId": "pi_abc123",
  "status": "SUCCESS",
  "paidAt": "2026-03-20T14:35:00Z"
}
```

- May send **duplicate callbacks** (simulates real-world webhook retries) — the system must handle this idempotently.

### 8.3 Retry Simulation

The dummy server can also be configured to:
- Never send a callback (simulates timeout — triggers the 10-min expiry flow).
- Send callback after the reservation window (simulates late callback — tests the expiry race condition).
- Send multiple callbacks with different statuses for the same paymentIntentId (edge case testing).

---

## 9. Cleanup & Expiry

### 9.1 Cron Worker — BookingIntent Cleanup

Runs every **1 minute** (configurable). Logic:

```
1. SELECT * FROM booking_intent_cleanup WHERE expiry <= NOW() LIMIT 100
2. For each expired entry:
   a. BEGIN transaction
   b. SELECT * FROM booking_intents WHERE bookingIntentId = ? FOR UPDATE
   c. IF status IN (CONFIRMED, EXPIRED):        -- already terminal
        - DELETE cleanup row (nothing else to do)

   d. IF status IN (PENDING, PAYMENT_FAILED):   -- non-terminal, but is it truly expired?
        - Read booking_intents.expiresAt (the source of truth — updated on every retry)

        - IF expiresAt > NOW():
            The booking was extended by a retry since the cleanup row was created.
            Cleanup row has a stale expiry. Do NOT expire the booking.
            → UPDATE booking_intent_cleanup SET expiry = booking_intents.expiresAt
            → COMMIT (skip this entry, it will be picked up again at the real expiry)

        - IF expiresAt <= NOW():
            Truly expired — proceed with cleanup.
            → UPDATE booking_intents SET status = 'EXPIRED'
            → UPDATE payment_intents SET status = 'EXPIRED'
              WHERE bookingIntentId = ? AND status = 'INITIATED'  -- expires ALL pending PIs
            → Remove seats from flights.reservedSeats
            → IF flight.status = 'FULLY_BOOKED' AND now has free seats
                → UPDATE flights SET status = 'SCHEDULED'
            → DELETE cleanup row

   e. COMMIT
```

**Why re-check `expiresAt`?** The BookingIntentCleanup table's `expiry` is set at booking creation time and updated on each retry. But there's a subtle race: a retry could extend `expiresAt` on the BookingIntent *after* the Cron Worker already fetched the cleanup row (the `SELECT` in step 1 runs outside the per-row transaction). By re-checking the authoritative `expiresAt` from the BookingIntent row *inside the `FOR UPDATE` lock*, we guarantee we never expire a booking that still has time left. If the expiry was extended, we sync the cleanup table and move on.

### 9.2 Why a Separate Cleanup Table?

- Avoids scanning the entire `booking_intents` table for expired rows.
- The `BookingIntentCleanup` table is small and indexed on `expiry` — efficient range scan.
- Decouples cleanup logic from booking logic.

---

## 10. API Design

### 10.1 Flight APIs

```
GET /api/flights?from={origin}&to={dest}&date={date}&minPrice=&maxPrice=
  → Returns list of flights with available seat maps

GET /api/flights/:flightId
  → Returns flight details with full seat map (reserved/available)
```

### 10.2 Booking APIs

```
POST /api/bookings
  Body: { flightId, seats: ["L1", "L2"], customerDetails: {...} }
  → Creates BookingIntent (PENDING), initiates payment
  → Returns: { bookingIntentId, status: "PENDING", expiresAt }

GET /api/bookings/:bookingIntentId
  → Returns booking status + details (for polling)
  → Client uses this to detect PAYMENT_FAILED and offer retry UI

POST /api/bookings/:bookingIntentId/retry-payment
  → Only allowed when status = PENDING or PAYMENT_FAILED AND retryCount < 3
  → Creates new PaymentIntent, extends reservation by +2 min
  → Returns: { bookingIntentId, newPaymentIntentId, status: "PENDING", expiresAt, retriesRemaining }
```

### 10.3 Webhook API (Internal)

```
POST /webhooks/payment
  Body: { paymentIntentId, status, paidAt? }
  → Webhook Service publishes to Kafka
  → Returns: 200 OK (acknowledged)
```

---

## 11. Key Design Decisions Summary

| Decision | Choice | Rationale |
|---|---|---|
| Primary DB | PostgreSQL | ACID transactions, `FOR UPDATE` row locking for race conditions |
| Caching | Redis | Low-latency flight search, hot source/destination keys |
| Cache sync | CDC (Debezium) | Decoupled, near-real-time sync from Postgres → Redis |
| Async processing | Kafka | Reliable event delivery for payment callbacks |
| Race condition strategy | Postgres `FOR UPDATE` | DB-level locking is the primary safeguard; simpler than distributed locks for our use case |
| Payment simulation | Dummy server with webhook | Drives realistic async status changes without real gateway dependency |
| Seat model | 30 fixed seats per flight | Simplified model (L, M, N rows × 10 seats each) |
| Retry policy | Max 3 retries, +2 min per retry | Prevents indefinite seat holding; max window ~16 min |
| Multi-PI strategy | Book on first active SUCCESS, refund the rest | Guarantees exactly one charge per booking, automatic refund for stale payments |
| Hot key detection | Dynamic, based on search counter metrics in Redis | No hardcoding; adapts to seasonal/trending routes automatically |
| Cleanup | Separate table + Cron | Efficient expiry scanning without full table scans |

---

## 12. Sequence Diagram — End to End

```
User        Client       API GW      Booking Svc     Postgres     Dummy Payment    Webhook Svc    Kafka    Booking Worker
 │            │            │              │              │              │                │           │            │
 │──select───▶│            │              │              │              │                │           │            │
 │  seats     │──POST /bookings──────────▶│              │              │                │           │            │
 │            │            │              │──BEGIN TX────▶│              │                │           │            │
 │            │            │              │  FOR UPDATE   │              │                │           │            │
 │            │            │              │  add to       │              │                │           │            │
 │            │            │              │  reservedSeats│              │                │           │            │
 │            │            │              │  insert BI    │              │                │           │            │
 │            │            │              │  (PENDING)    │              │                │           │            │
 │            │            │              │  insert PI    │              │                │           │            │
 │            │            │              │  (INITIATED)  │              │                │           │            │
 │            │            │              │──COMMIT──────▶│              │                │           │            │
 │            │            │              │              │              │                │           │            │
 │            │            │              │──POST /create-payment──────▶│                │           │            │
 │            │            │              │◀─── 200 ACK ────────────────│                │           │            │
 │            │◀── { bookingIntentId } ───│              │              │                │           │            │
 │◀─ show ───│            │              │              │              │                │           │            │
 │  status    │            │              │              │              │                │           │            │
 │            │            │              │              │    (random delay)             │           │            │
 │            │            │              │              │              │                │           │            │
 │            │            │              │              │              │──POST webhook─▶│           │            │
 │            │            │              │              │              │                │──publish─▶│            │
 │            │            │              │              │              │                │           │──consume──▶│
 │            │            │              │              │              │                │           │            │
 │            │            │              │              │              │                │           │  BEGIN TX   │
 │            │            │              │              │◀─── FOR UPDATE ───────────────────────────────────────│
 │            │            │              │              │──── row data ─────────────────────────────────────────▶│
 │            │            │              │              │◀─── UPDATE BI status=CONFIRMED ───────────────────────│
 │            │            │              │              │◀─── Move seats: reserved → booked ────────────────────│
 │            │            │              │              │──── COMMIT ───────────────────────────────────────────▶│
 │            │            │              │              │              │                │           │            │
 │            │──GET /bookings/:id───────▶│              │              │                │           │            │
 │            │◀── { status: CONFIRMED } ─│              │              │                │           │            │
 │◀─ done ───│            │              │              │              │                │           │            │
```
