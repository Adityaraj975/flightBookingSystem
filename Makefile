.PHONY: infra-up infra-down infra-clean migrate seed api booking-worker cron-worker cdc-worker payment-server dev setup

# Use docker-compose if available (e.g. older Docker), else docker compose
DOCKER_COMPOSE := $(shell command -v docker-compose 2>/dev/null || echo "docker compose")
COMPOSE_UP = $(DOCKER_COMPOSE) up -d
COMPOSE_DOWN = $(DOCKER_COMPOSE) down

# Start Postgres, Redis, Kafka. Run 'make migrate' once after (or use 'make setup' for full first-time setup).
infra-up:
	$(COMPOSE_UP)
	@echo "Waiting for services to be healthy..."
	@sleep 5

infra-down:
	$(COMPOSE_DOWN) -v

# Remove containers and volumes (full reset, no data restored).
# Use if you get "container name already in use" or want a clean slate.
infra-clean:
	$(COMPOSE_DOWN) -v
	-docker rm -f fb-postgres fb-redis fb-kafka 2>/dev/null || true
	@echo "Containers and volumes removed. Run make infra-up then make migrate."

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
	@echo "Infrastructure ready. Run 'make dev' to start all services."
