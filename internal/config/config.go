// Package config loads configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all service configurations.
type Config struct {
	Port            int           `json:"port"`
	ReadTimeout     time.Duration `json:"read_timeout"`
	WriteTimeout    time.Duration `json:"write_timeout"`
	IdleTimeout     time.Duration `json:"idle_timeout"`
	DefaultProvider string        `json:"default_provider"`

	// Upstream Ollama configuration
	OllamaBaseURL string        `json:"ollama_base_url"`
	OllamaTimeout time.Duration `json:"ollama_timeout"`

	// Mock Provider configuration
	MockLatencyMin     time.Duration `json:"mock_latency_min"`
	MockLatencyMax     time.Duration `json:"mock_latency_max"`
	MockErrorRate      float64       `json:"mock_error_rate"`
	MockStallForever   bool          `json:"mock_stall_forever"`
	MockStreamChunkLen int           `json:"mock_stream_chunk_len"`
	MockChunkDelay     time.Duration `json:"mock_chunk_delay"`

	// Circuit breaker configuration
	BreakerWindowSize        int           `json:"breaker_window_size"`
	BreakerFailureThreshold  float64       `json:"breaker_failure_threshold"`
	BreakerMinRequests       int           `json:"breaker_min_requests"`
	BreakerCooldown          time.Duration `json:"breaker_cooldown"`
	BreakerMaxHalfOpenProbes int           `json:"breaker_max_half_open_probes"`

	// Retry configuration
	RetryMaxAttempts    int           `json:"retry_max_attempts"`
	RetryAttemptTimeout time.Duration `json:"retry_attempt_timeout"`
	RetryBaseDelay      time.Duration `json:"retry_base_delay"`
	RetryMaxDelay       time.Duration `json:"retry_max_delay"`

	// Infrastructure config (used in later phases)
	DatabaseURL string `json:"database_url"`
	RedisURL    string `json:"redis_url"`
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{
		Port:               getEnvInt("PORT", 8080),
		ReadTimeout:        getEnvDuration("READ_TIMEOUT", 30*time.Second),
		WriteTimeout:       getEnvDuration("WRITE_TIMEOUT", 120*time.Second),
		IdleTimeout:        getEnvDuration("IDLE_TIMEOUT", 60*time.Second),
		DefaultProvider:    getEnv("DEFAULT_PROVIDER", "mock"),
		OllamaBaseURL:      getEnv("OLLAMA_BASE_URL", "http://localhost:11434"),
		OllamaTimeout:      getEnvDuration("OLLAMA_TIMEOUT", 60*time.Second),
		MockLatencyMin:     getEnvDuration("MOCK_LATENCY_MIN", 0),
		MockLatencyMax:     getEnvDuration("MOCK_LATENCY_MAX", 0),
		MockErrorRate:      getEnvFloat("MOCK_ERROR_RATE", 0.0),
		MockStallForever:   getEnvBool("MOCK_STALL_FOREVER", false),
		MockStreamChunkLen: getEnvInt("MOCK_STREAM_CHUNK_LEN", 5),
		MockChunkDelay:     getEnvDuration("MOCK_CHUNK_DELAY", 10*time.Millisecond),

		BreakerWindowSize:        getEnvInt("BREAKER_WINDOW_SIZE", 20),
		BreakerFailureThreshold:  getEnvFloat("BREAKER_FAILURE_THRESHOLD", 0.5),
		BreakerMinRequests:       getEnvInt("BREAKER_MIN_REQUESTS", 5),
		BreakerCooldown:          getEnvDuration("BREAKER_COOLDOWN", 10*time.Second),
		BreakerMaxHalfOpenProbes: getEnvInt("BREAKER_MAX_HALF_OPEN_PROBES", 1),

		RetryMaxAttempts:    getEnvInt("RETRY_MAX_ATTEMPTS", 3),
		RetryAttemptTimeout: getEnvDuration("RETRY_ATTEMPT_TIMEOUT", 30*time.Second),
		RetryBaseDelay:      getEnvDuration("RETRY_BASE_DELAY", 100*time.Millisecond),
		RetryMaxDelay:       getEnvDuration("RETRY_MAX_DELAY", 30*time.Second),

		DatabaseURL: getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/relay?sslmode=disable"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379/0"),
	}

	if cfg.MockLatencyMin > cfg.MockLatencyMax && cfg.MockLatencyMax > 0 {
		return nil, fmt.Errorf("MOCK_LATENCY_MIN (%v) cannot be greater than MOCK_LATENCY_MAX (%v)", cfg.MockLatencyMin, cfg.MockLatencyMax)
	}

	if cfg.MockErrorRate < 0.0 || cfg.MockErrorRate > 1.0 {
		return nil, fmt.Errorf("MOCK_ERROR_RATE (%v) must be between 0.0 and 1.0", cfg.MockErrorRate)
	}

	if cfg.BreakerFailureThreshold <= 0 || cfg.BreakerFailureThreshold > 1.0 {
		return nil, fmt.Errorf("BREAKER_FAILURE_THRESHOLD (%v) must be in (0, 1]", cfg.BreakerFailureThreshold)
	}

	if cfg.RetryMaxAttempts < 1 {
		return nil, fmt.Errorf("RETRY_MAX_ATTEMPTS must be >= 1, got %d", cfg.RetryMaxAttempts)
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && strings.TrimSpace(val) != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func getEnvFloat(key string, defaultVal float64) float64 {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := strconv.ParseBool(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return defaultVal
}

func getEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		if parsed, err := time.ParseDuration(strings.TrimSpace(val)); err == nil {
			return parsed
		}
	}
	return defaultVal
}
