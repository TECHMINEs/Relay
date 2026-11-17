// Package provider provides implementations of the Provider interface.
package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/TECHMINEs/Relay/internal/config"
)

// MockProvider implements Provider for testing, benchmarking, and controlled fault injection.
type MockProvider struct {
	cfg         *config.Config
	callCount   atomic.Int64
	cancelCount atomic.Int64
	errorRate   atomic.Uint64 // stores math.Float64bits(rate)
}

// NewMockProvider creates a new MockProvider instance configured via Config.
func NewMockProvider(cfg *config.Config) *MockProvider {
	m := &MockProvider{
		cfg: cfg,
	}
	m.errorRate.Store(math.Float64bits(cfg.MockErrorRate))
	return m
}

// Name returns the provider name.
func (m *MockProvider) Name() string {
	return "mock"
}

// CallCount returns total chat/stream invocations.
func (m *MockProvider) CallCount() int64 {
	return m.callCount.Load()
}

// CancelCount returns total calls cancelled by context.
func (m *MockProvider) CancelCount() int64 {
	return m.cancelCount.Load()
}

// ErrorRate returns the current simulated error rate.
func (m *MockProvider) ErrorRate() float64 {
	return math.Float64frombits(m.errorRate.Load())
}

// SetErrorRate dynamically updates the simulated error rate safely.
func (m *MockProvider) SetErrorRate(rate float64) {
	if rate < 0 {
		rate = 0
	} else if rate > 1 {
		rate = 1
	}
	m.errorRate.Store(math.Float64bits(rate))
}

func (m *MockProvider) applyLatencyAndFaults(ctx context.Context) error {
	m.callCount.Add(1)

	if m.cfg.MockStallForever {
		<-ctx.Done()
		m.cancelCount.Add(1)
		return ctx.Err()
	}

	if m.cfg.MockLatencyMax > 0 {
		var waitDur time.Duration
		if m.cfg.MockLatencyMax > m.cfg.MockLatencyMin {
			diff := m.cfg.MockLatencyMax - m.cfg.MockLatencyMin
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(diff)))
			waitDur = m.cfg.MockLatencyMin + time.Duration(n.Int64())
		} else {
			waitDur = m.cfg.MockLatencyMin
		}

		select {
		case <-time.After(waitDur):
		case <-ctx.Done():
			m.cancelCount.Add(1)
			return ctx.Err()
		}
	}

	curRate := m.ErrorRate()
	if curRate > 0 {
		n, _ := rand.Int(rand.Reader, big.NewInt(10000))
		threshold := int64(curRate * 10000)
		if n.Int64() < threshold {
			return ErrSimulatedFailure
		}
	}

	return nil
}

// Chat produces an OpenAI-compatible non-streaming chat response.
func (m *MockProvider) Chat(ctx context.Context, req *ChatRequest) (*Response, error) {
	if err := m.applyLatencyAndFaults(ctx); err != nil {
		return nil, err
	}

	id := generateID("chatcmpl-mock-")
	now := time.Now().Unix()

	resp := &Response{
		ID:      id,
		Object:  "chat.completion",
		Created: now,
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: fmt.Sprintf("Mock response to %d messages for model %s", len(req.Messages), req.Model),
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     10 * len(req.Messages),
			CompletionTokens: 20,
			TotalTokens:      10*len(req.Messages) + 20,
		},
	}

	return resp, nil
}

// ChatStream produces an OpenAI-compatible stream of chunk tokens.
func (m *MockProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	if m.cfg.MockStallForever {
		m.callCount.Add(1)
		ch := make(chan Chunk)
		go func() {
			<-ctx.Done()
			m.cancelCount.Add(1)
			close(ch)
		}()
		return ch, nil
	}

	if err := m.applyLatencyAndFaults(ctx); err != nil {
		return nil, err
	}

	ch := make(chan Chunk, 10)
	id := generateID("chatcmpl-mock-")
	now := time.Now().Unix()

	go func() {
		defer close(ch)

		// The first chunk carries the role.
		firstChunk := Chunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: now,
			Model:   req.Model,
			Choices: []ChunkChoice{
				{
					Index: 0,
					Delta: ChunkDelta{
						Role: "assistant",
					},
					FinishReason: nil,
				},
			},
		}

		select {
		case <-ctx.Done():
			m.cancelCount.Add(1)
			return
		case ch <- firstChunk:
		}

		words := []string{"Hello", " from", " Relay", " mock", " provider", " streaming", "!"}
		for i, word := range words {
			if m.cfg.MockChunkDelay > 0 {
				select {
				case <-time.After(m.cfg.MockChunkDelay):
				case <-ctx.Done():
					m.cancelCount.Add(1)
					return
				}
			}

			chunk := Chunk{
				ID:      id,
				Object:  "chat.completion.chunk",
				Created: now,
				Model:   req.Model,
				Choices: []ChunkChoice{
					{
						Index: 0,
						Delta: ChunkDelta{
							Content: word,
						},
						FinishReason: nil,
					},
				},
			}

			if i == len(words)-1 {
				stop := "stop"
				chunk.Choices[0].FinishReason = &stop
			}

			select {
			case <-ctx.Done():
				m.cancelCount.Add(1)
				return
			case ch <- chunk:
			}
		}
	}()

	return ch, nil
}

// Embeddings returns mock vector embeddings.
func (m *MockProvider) Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	if err := m.applyLatencyAndFaults(ctx); err != nil {
		return nil, err
	}

	resp := &EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Data:   make([]EmbeddingData, len(req.Input)),
		Usage: Usage{
			PromptTokens: 5 * len(req.Input),
			TotalTokens:  5 * len(req.Input),
		},
	}

	for i := range req.Input {
		resp.Data[i] = EmbeddingData{
			Index:     i,
			Object:    "embedding",
			Embedding: []float64{0.0123, -0.0456, 0.0789, 0.1011},
		}
	}

	return resp, nil
}

func generateID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
