#!/usr/bin/env bash
# Simulate payment webhook race conditions and report outcome.
# Requires: API on BASE_URL, booking-worker running, curl, jq.
# Usage: ./scripts/race_simulation.sh [base_url]
#   base_url defaults to http://localhost:8080

set -e
BASE_URL="${1:-http://localhost:8080}"
API="${BASE_URL}/api"
WEBHOOK="${BASE_URL}/webhooks/payment"

echo "=== Payment webhook race condition simulation ==="
echo "API: $BASE_URL"
echo ""

# 1) Get a flight
echo "[1] Fetching a flight (DEL → BOM, tomorrow)..."
TOMORROW=$(date -u -v+1d +%Y-%m-%d 2>/dev/null || date -u -d "+1 day" +%Y-%m-%d 2>/dev/null || date -u +%Y-%m-%d)
FLIGHT_RESP=$(curl -s "${API}/flights?from=DEL&to=BOM&date=${TOMORROW}")
FLIGHT_ID=$(echo "$FLIGHT_RESP" | jq -r '.flights[0].id')
if [[ "$FLIGHT_ID" == "null" || -z "$FLIGHT_ID" ]]; then
  echo "No flight found. Try another date or run seed first."
  exit 1
fi
echo "    Flight ID: $FLIGHT_ID"

# 2) Create booking (first payment intent)
echo "[2] Creating booking (seats L5, L6)..."
BOOK_RESP=$(curl -s -X POST "${API}/bookings" \
  -H "Content-Type: application/json" \
  -d '{
    "flightId":"'"$FLIGHT_ID"'",
    "seats":["L5","L6"],
    "userId":"550e8400-e29b-41d4-a716-446655440000",
    "customerDetails":{"name":"Race Test","email":"race@test.com"}
  }')
BOOKING_ID=$(echo "$BOOK_RESP" | jq -r '.bookingIntentId')
if [[ "$BOOKING_ID" == "null" || -z "$BOOKING_ID" ]]; then
  echo "Create booking failed: $BOOK_RESP"
  exit 1
fi
echo "    Booking ID: $BOOKING_ID"

# 3) Get first payment intent ID
BI=$(curl -s "${API}/bookings/${BOOKING_ID}")
PI_1=$(echo "$BI" | jq -r '.activePaymentIntentId')
echo "[3] First payment intent: $PI_1"

# 4) Retry to get second payment intent
echo "[4] Retrying payment to create second intent..."
RETRY_RESP=$(curl -s -X POST "${API}/bookings/${BOOKING_ID}/retry")
PI_2=$(echo "$RETRY_RESP" | jq -r '.newPaymentIntentId')
if [[ "$PI_2" == "null" || -z "$PI_2" ]]; then
  echo "Retry failed (may have hit max retries or terminal state): $RETRY_RESP"
  echo "Using single-PI duplicate delivery scenario instead."
  PI_2="$PI_1"
  DUPLICATE_ONLY=1
else
  echo "    Second payment intent: $PI_2"
  DUPLICATE_ONLY=0
fi

# 5) Fire both SUCCESS callbacks in parallel (race)
echo "[5] Sending SUCCESS webhooks in parallel (race)..."
payload_1=$(jq -n --arg pi "$PI_1" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{paymentIntentId:$pi, status:"SUCCESS", paidAt:$t}')
payload_2=$(jq -n --arg pi "$PI_2" --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{paymentIntentId:$pi, status:"SUCCESS", paidAt:$t}')

curl -s -X POST "$WEBHOOK" -H "Content-Type: application/json" -d "$payload_1" &
curl -s -X POST "$WEBHOOK" -H "Content-Type: application/json" -d "$payload_2" &
wait
echo "    Both webhooks sent."

# 6) Poll until terminal state
echo "[6] Polling booking until terminal state..."
i=0
while [ "$i" -lt 20 ]; do
  i=$((i + 1))
  BI=$(curl -s "${API}/bookings/${BOOKING_ID}")
  STATUS=$(echo "$BI" | jq -r '.status')
  echo "    Poll $i: status=$STATUS"
  case "$STATUS" in
    CONFIRMED|EXPIRED|PAYMENT_FAILED) break ;;
  esac
  sleep 1
done

# 7) Report outcome
echo ""
echo "=== Outcome ==="
BI=$(curl -s "${API}/bookings/${BOOKING_ID}")
echo "$BI" | jq '{
  bookingIntentId,
  status,
  activePaymentIntentId,
  retryCount,
  seats
}'

WINNER_PI=$(echo "$BI" | jq -r '.activePaymentIntentId')
FINAL_STATUS=$(echo "$BI" | jq -r '.status')
echo ""
echo "--- Who won? ---"
if [[ "$FINAL_STATUS" == "CONFIRMED" ]]; then
  echo "  Winner (confirmed the booking): $WINNER_PI"
  echo "  Loser(s): triggered REFUND — see booking-worker logs for 'REFUND: payment_intent_id=...'"
else
  echo "  Final status: $FINAL_STATUS (no winner; check worker logs)"
fi
echo ""
echo "Interpretation:"
echo "  - Two SUCCESS callbacks were sent at once. The one processed first with booking still PENDING won."
echo "  - The other callback saw CONFIRMED (or stale active PI) and triggered a REFUND."
if [[ "$DUPLICATE_ONLY" == 1 ]]; then
  echo "  - (Same PI was sent twice; idempotent handling: first confirms, second REFUND.)"
fi
