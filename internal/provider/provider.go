// Package provider defines interfaces and models for LLM upstream providers.
package provider

import (
	"context"
	"errors"
)

// Common provider errors.
var (
	ErrProviderStalled    = errors.New("provider stalled indefinitely")
	ErrSimulatedFailure   = errors.New("simulated provider error")
	ErrModelNotSupported  = errors.New("requested model is not supported")
	ErrInvalidRequest     = errors.New("invalid request parameters")
	ErrProviderConnection = errors.New("failed to connect to upstream provider")
)

// ChatMessage represents a single message in a chat completion request or response.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ChatRequest represents an OpenAI-compatible chat completion request payload.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
	TopP        *float64      `json:"top_p,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
}

// Usage captures token usage statistics.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatChoice represents a completion choice in a non-streaming response.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// Response represents an OpenAI-compatible chat completion response payload.
type Response struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
}

// ChunkDelta represents the incremental delta in a streaming chunk.
type ChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// ChunkChoice represents a choice in a streaming chunk.
type ChunkChoice struct {
	Index        int        `json:"index"`
	Delta        ChunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

// Chunk represents an OpenAI-compatible SSE chunk data payload.
type Chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
	Err     error         `json:"-"`
}

// EmbeddingRequest represents a vector embedding request.
type EmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// EmbeddingData represents single embedding vector item.
type EmbeddingData struct {
	Index     int       `json:"index"`
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
}

// EmbeddingResponse represents OpenAI-compatible embeddings response.
type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  Usage           `json:"usage"`
}

// Provider defines the standard contract implemented by upstream LLM backends.
type Provider interface {
	// Name returns the identifier for this provider.
	Name() string

	// Chat sends a non-streaming chat completion request.
	Chat(ctx context.Context, req *ChatRequest) (*Response, error)

	// ChatStream initiates a streaming chat completion, streaming chunks over a channel.
	ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error)

	// Embeddings generates vector embeddings for given inputs.
	Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
}
