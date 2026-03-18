# Scripts

## Race condition simulation

Simulate payment webhook race conditions to verify "book on first success, refund the rest" behavior.

### Prerequisites

- API server running (`make api`)
- Booking worker running (`make booking-worker`)
- `curl` and `jq` installed

### Usage

```bash
chmod +x scripts/race_simulation.sh
./scripts/race_simulation.sh                    # use http://localhost:8080
./scripts/race_simulation.sh http://localhost:8080
```

### What it does

1. Fetches a flight (DEL → BOM, tomorrow) and creates a booking (seats L1, L2).
2. Gets the first payment intent ID from the booking.
3. Calls **Retry payment** to create a second payment intent for the same booking.
4. Sends **two SUCCESS webhooks in parallel** (one per payment intent) to simulate a race.
5. Polls the booking until it reaches a terminal state (CONFIRMED / EXPIRED / PAYMENT_FAILED).
6. Prints the outcome: `bookingIntentId`, `status`, `activePaymentIntentId`, `seats`.

### How to interpret the result

- **Status CONFIRMED**: One of the two callbacks was processed first and confirmed the booking; the other is treated as stale and triggers a **REFUND** (see booking-worker logs for `REFUND: payment_intent_id=pi_xxx`).
- **activePaymentIntentId** in the final response is the payment intent that was *current* when the booking was confirmed (the “winner” from the app’s perspective).
- Check **booking-worker** logs to see which payment intent caused a REFUND (the “loser”).

### Duplicate delivery

If retry is not possible (e.g. max retries already used), the script sends the *same* payment intent SUCCESS twice in parallel. The handler is idempotent: the first callback confirms, the second sees the booking already CONFIRMED and triggers REFUND.
