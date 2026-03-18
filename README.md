# Flight Booking System

Local end-to-end flight booking system (Go, Postgres, Redis, Kafka) per LLD.

## Prerequisites

- Go 1.22+
- Docker & Docker Compose
- Make

## Run the whole system (5 steps)

1. **Clone and install deps** — From the repo root, run `go mod tidy` to download Go dependencies.

2. **Start infrastructure** — Run `make infra-up` to start Postgres, Redis, and Kafka in Docker. (Use `docker-compose up -d` if your Docker doesn’t support `docker compose`.) If you see “container name already in use”, run `make infra-clean` then `make infra-up` (stopped containers still keep their names until removed).

3. **Run migrations** — Run `make migrate` once so the database has all tables and triggers. (Needs the `fb-postgres` container up.)

4. **Start all services** — Run `make dev` to start the API server, dummy payment server, booking worker, cron worker, and CDC worker in the background.

5. **Use the app** — Open http://localhost:8080/ in a browser: search flights, pick a flight and seats, book, and watch the booking status (and use “Refresh status” for session bookings).

That’s it. The API will seed flights automatically on first start if the `flights` table is empty.

## Setup (detailed)

1. **Install Go dependencies**

   ```bash
   go mod tidy
   ```

2. **Start infrastructure** (Postgres, Redis, Kafka)

   ```bash
   make infra-up
   ```
   If you use older Docker, run `docker-compose up -d` instead.

3. **Run migrations**

   ```bash
   make migrate
   ```
   (Requires `fb-postgres` container running; see `docker-compose.yml`.)

4. **Seed flights** — Optional. The API **seeds automatically on startup** if the `flights` table is empty, so you can skip this and just start the API.
   ```bash
   make seed
   ```

## Run

- **All services (API, workers, payment server):**  
  ```bash
  make dev
  ```
  Or run in separate terminals:
  - `make api` — API server (:8080)
  - `make payment-server` — Dummy payment server (:8081)
  - `make booking-worker` — Kafka consumer for payment callbacks
  - `make cron-worker` — Cleanup, hot-key refresh, departure sweep
  - `make cdc-worker` — Postgres LISTEN → Redis cache sync

- **UI:** Open http://localhost:8080/ (served by the API).

## Quick test

1. Open http://localhost:8080/
2. Choose From, To, Date and click Search.
3. Click a flight, select seats, click "Book selected seats".
4. Watch the booking status (polls every 2s). Use "Retry payment" if status is PAYMENT_FAILED.

## Config

Copy `.env.example` to `.env` and adjust. Defaults work with `make infra-up` (Postgres, Redis, Kafka on localhost).

---

## Hot cache refresh

Every time someone searches for flights (e.g. Delhi → Mumbai on a date), we increment a counter in Redis for that origin, that destination, and that date. Every few minutes the cron worker looks at these counters: if an origin or destination has been searched more than a set number of times (the “threshold”), we mark it as “hot”. For hot routes we then pre-load all matching flights from the database into Redis (by origin+date or destination+date) so the next search is served from cache and is faster. So the system automatically treats busy routes as hot and caches them; quieter routes are still served from the database or a smaller cache when needed.
