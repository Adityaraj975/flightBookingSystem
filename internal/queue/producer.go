package queue

import (
	"context"
	"encoding/json"

	"flight-booking/internal/config"

	"github.com/segmentio/kafka-go"
)

type PaymentCallbackEvent struct {
	PaymentIntentID string `json:"paymentIntentId"`
	Status          string `json:"status"`
	PaidAt          string `json:"paidAt,omitempty"`
}

type Producer struct {
	writer *kafka.Writer
	topic  string
}

func NewProducer(cfg *config.Config) *Producer {
	w := &kafka.Writer{
		Addr:     kafka.TCP(cfg.KafkaBrokers),
		Topic:    cfg.KafkaTopicPaymentCallbacks,
		Balancer: &kafka.Hash{},
	}
	return &Producer{writer: w, topic: cfg.KafkaTopicPaymentCallbacks}
}

func (p *Producer) PublishPaymentCallback(ctx context.Context, event PaymentCallbackEvent) error {
	value, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return p.writer.WriteMessages(ctx, kafka.Message{
		Key:   []byte(event.PaymentIntentID),
		Value: value,
	})
}

func (p *Producer) Close() error {
	return p.writer.Close()
}
