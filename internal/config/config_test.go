package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		expectedErr bool
		check       func(t *testing.T, cfg *Config)
	}{
		{
			name:        "default values",
			env:         map[string]string{},
			expectedErr: false,
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != 8080 {
					t.Errorf("expected port 8080, got %d", cfg.Port)
				}
				if cfg.DefaultProvider != "mock" {
					t.Errorf("expected default provider mock, got %s", cfg.DefaultProvider)
				}
				if cfg.OllamaBaseURL != "http://localhost:11434" {
					t.Errorf("expected ollama url http://localhost:11434, got %s", cfg.OllamaBaseURL)
				}
			},
		},
		{
			name: "custom environment values",
			env: map[string]string{
				"PORT":               "9090",
				"DEFAULT_PROVIDER":   "ollama",
				"MOCK_LATENCY_MIN":   "10ms",
				"MOCK_LATENCY_MAX":   "50ms",
				"MOCK_ERROR_RATE":    "0.25",
				"MOCK_STALL_FOREVER": "true",
			},
			expectedErr: false,
			check: func(t *testing.T, cfg *Config) {
				if cfg.Port != 9090 {
					t.Errorf("expected port 9090, got %d", cfg.Port)
				}
				if cfg.DefaultProvider != "ollama" {
					t.Errorf("expected default provider ollama, got %s", cfg.DefaultProvider)
				}
				if cfg.MockLatencyMin != 10*time.Millisecond {
					t.Errorf("expected min latency 10ms, got %v", cfg.MockLatencyMin)
				}
				if cfg.MockLatencyMax != 50*time.Millisecond {
					t.Errorf("expected max latency 50ms, got %v", cfg.MockLatencyMax)
				}
				if cfg.MockErrorRate != 0.25 {
					t.Errorf("expected error rate 0.25, got %v", cfg.MockErrorRate)
				}
				if !cfg.MockStallForever {
					t.Errorf("expected stall forever true, got %v", cfg.MockStallForever)
				}
			},
		},
		{
			name: "invalid latency range",
			env: map[string]string{
				"MOCK_LATENCY_MIN": "100ms",
				"MOCK_LATENCY_MAX": "10ms",
			},
			expectedErr: true,
			check:       nil,
		},
		{
			name: "invalid error rate",
			env: map[string]string{
				"MOCK_ERROR_RATE": "1.5",
			},
			expectedErr: true,
			check:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save current env and restore after test
			origEnv := make(map[string]string)
			for k := range tt.env {
				origEnv[k] = os.Getenv(k)
				os.Setenv(k, tt.env[k])
			}
			defer func() {
				for k := range tt.env {
					if origVal, exists := origEnv[k]; exists && origVal != "" {
						os.Setenv(k, origVal)
					} else {
						os.Unsetenv(k)
					}
				}
			}()

			cfg, err := Load()
			if (err != nil) != tt.expectedErr {
				t.Fatalf("expected error: %v, got error: %v", tt.expectedErr, err)
			}
			if !tt.expectedErr && tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
