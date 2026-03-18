package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	RedisAddr string

	KafkaBrokers             string
	KafkaTopicPaymentCallbacks string
	KafkaConsumerGroup       string

	APIPort string

	PaymentServerPort       int
	PaymentCallbackBaseURL  string
	PaymentMinDelaySec       int
	PaymentMaxDelaySec       int
	PaymentSuccessRate       int
	PaymentDuplicateRate    int
	PaymentNoCallbackRate   int

	InitialReservationMinutes int
	RetryExtensionMinutes     int
	MaxRetries                int

	SourceThreshold      int
	DestinationThreshold int
	HotKeyTTLSec         int
	MetricTTLSec         int

	CleanupIntervalSec         int
	HotKeyRefreshIntervalSec   int
	DepartureSweepIntervalSec  int
}

func Load() *Config {
	_ = godotenv.Load()

	getEnv := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	getInt := func(key string, def int) int {
		if v := os.Getenv(key); v != "" {
			if i, err := strconv.Atoi(v); err == nil {
				return i
			}
		}
		return def
	}

	return &Config{
		DBHost:     getEnv("DB_HOST", "localhost"),
		DBPort:     getEnv("DB_PORT", "5432"),
		DBUser:     getEnv("DB_USER", "flight_user"),
		DBPassword: getEnv("DB_PASSWORD", "flight_pass"),
		DBName:     getEnv("DB_NAME", "flight_booking"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),

		RedisAddr: getEnv("REDIS_ADDR", "localhost:6379"),

		KafkaBrokers:               getEnv("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopicPaymentCallbacks: getEnv("KAFKA_TOPIC_PAYMENT_CALLBACKS", "payment.callbacks"),
		KafkaConsumerGroup:         getEnv("KAFKA_CONSUMER_GROUP", "booking-worker-group"),

		APIPort: getEnv("API_PORT", "8080"),

		PaymentServerPort:      getInt("PAYMENT_SERVER_PORT", 8081),
		PaymentCallbackBaseURL: getEnv("PAYMENT_CALLBACK_BASE_URL", "http://localhost:8080/webhooks/payment"),
		PaymentMinDelaySec:     getInt("PAYMENT_MIN_DELAY_SEC", 5),
		PaymentMaxDelaySec:     getInt("PAYMENT_MAX_DELAY_SEC", 120),
		PaymentSuccessRate:    getInt("PAYMENT_SUCCESS_RATE", 80),
		PaymentDuplicateRate:  getInt("PAYMENT_DUPLICATE_RATE", 10),
		PaymentNoCallbackRate: getInt("PAYMENT_NO_CALLBACK_RATE", 5),

		InitialReservationMinutes: getInt("INITIAL_RESERVATION_MINUTES", 10),
		RetryExtensionMinutes:     getInt("RETRY_EXTENSION_MINUTES", 2),
		MaxRetries:                getInt("MAX_RETRIES", 3),

		SourceThreshold:      getInt("SOURCE_THRESHOLD", 500),
		DestinationThreshold: getInt("DESTINATION_THRESHOLD", 500),
		HotKeyTTLSec:         getInt("HOT_KEY_TTL_SEC", 300),
		MetricTTLSec:         getInt("METRIC_TTL_SEC", 86400),

		CleanupIntervalSec:        getInt("CLEANUP_INTERVAL_SEC", 60),
		HotKeyRefreshIntervalSec:  getInt("HOT_KEY_REFRESH_INTERVAL_SEC", 300),
		DepartureSweepIntervalSec: getInt("DEPARTURE_SWEEP_INTERVAL_SEC", 120),
	}
}
