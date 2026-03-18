package queue

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"flight-booking/internal/config"

	"github.com/segmentio/kafka-go"
)

type Consumer struct {
	reader *kafka.Reader
}

func NewConsumer(cfg *config.Config) *Consumer {
	r := kafka.NewReader(kafka.ReaderConfig{
		Brokers:        []string{cfg.KafkaBrokers},
		Topic:          cfg.KafkaTopicPaymentCallbacks,
		GroupID:        cfg.KafkaConsumerGroup,
		MinBytes:       1,
		MaxBytes:       10e6,
		CommitInterval: 0,
	})
	return &Consumer{reader: r}
}

func (c *Consumer) Start(ctx context.Context, handler func(PaymentCallbackEvent) error) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("kafka read error: %v", err)
			time.Sleep(backoff)
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		var event PaymentCallbackEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			log.Printf("kafka unmarshal error: %v", err)
			if err := c.reader.CommitMessages(ctx, msg); err != nil {
				log.Printf("kafka commit error (bad message): %v", err)
			}
			continue
		}
		if err := handler(event); err != nil {
			log.Printf("callback handler error for pi=%s: %v, will retry", event.PaymentIntentID, err)
			continue
		}
		if err := c.reader.CommitMessages(ctx, msg); err != nil {
			log.Printf("kafka commit error: %v", err)
		}
	}
}

func (c *Consumer) Close() error {
	return c.reader.Close()
}
