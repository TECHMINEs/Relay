// Package provider provides implementations of the Provider interface.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TECHMINEs/Relay/internal/config"
)

// OllamaProvider implements Provider for the Ollama local LLM server.
type OllamaProvider struct {
	baseURL    string
	httpClient *http.Client
}

// NewOllamaProvider creates a new OllamaProvider instance.
func NewOllamaProvider(cfg *config.Config) *OllamaProvider {
	baseURL := strings.TrimRight(cfg.OllamaBaseURL, "/")
	return &OllamaProvider{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: cfg.OllamaTimeout,
		},
	}
}

// Name returns the provider name.
func (o *OllamaProvider) Name() string {
	return "ollama"
}

// ollamaChatMessage represents Ollama's message schema.
type ollamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ollamaChatRequest represents Ollama's /api/chat request schema.
type ollamaChatRequest struct {
	Model    string              `json:"model"`
	Messages []ollamaChatMessage `json:"messages"`
	Stream   bool                `json:"stream"`
	Options  map[string]any      `json:"options,omitempty"`
}

// ollamaChatResponse represents Ollama's /api/chat response schema.
type ollamaChatResponse struct {
	Model           string            `json:"model"`
	CreatedAt       time.Time         `json:"created_at"`
	Message         ollamaChatMessage `json:"message"`
	Done            bool              `json:"done"`
	DoneReason      string            `json:"done_reason,omitempty"`
	TotalDuration   int64             `json:"total_duration,omitempty"`
	PromptEvalCount int               `json:"prompt_eval_count,omitempty"`
	EvalCount       int               `json:"eval_count,omitempty"`
	Error           string            `json:"error,omitempty"`
}

// ollamaEmbedRequest represents Ollama's /api/embed request schema.
type ollamaEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

// ollamaEmbedResponse represents Ollama's /api/embed response schema.
type ollamaEmbedResponse struct {
	Model      string      `json:"model"`
	Embeddings [][]float64 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// Chat sends a non-streaming chat completion to Ollama.
func (o *OllamaProvider) Chat(ctx context.Context, req *ChatRequest) (*Response, error) {
	messages := make([]ollamaChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ollamaChatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}

	ollamaReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   false,
	}

	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil {
		ollamaReq.Options = make(map[string]any)
		if req.Temperature != nil {
			ollamaReq.Options["temperature"] = *req.Temperature
		}
		if req.TopP != nil {
			ollamaReq.Options["top_p"] = *req.TopP
		}
		if req.MaxTokens != nil {
			ollamaReq.Options["num_predict"] = *req.MaxTokens
		}
	}

	bodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal request: %v", ErrInvalidRequest, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := o.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderConnection, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama returned error status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaChatResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode ollama response: %w", err)
	}

	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("ollama error: %s", ollamaResp.Error)
	}

	finishReason := "stop"
	if ollamaResp.DoneReason != "" {
		finishReason = ollamaResp.DoneReason
	}

	id := generateID("chatcmpl-ollama-")
	now := ollamaResp.CreatedAt.Unix()
	if now <= 0 {
		now = time.Now().Unix()
	}

	return &Response{
		ID:      id,
		Object:  "chat.completion",
		Created: now,
		Model:   req.Model,
		Choices: []ChatChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    ollamaResp.Message.Role,
					Content: ollamaResp.Message.Content,
				},
				FinishReason: finishReason,
			},
		},
		Usage: Usage{
			PromptTokens:     ollamaResp.PromptEvalCount,
			CompletionTokens: ollamaResp.EvalCount,
			TotalTokens:      ollamaResp.PromptEvalCount + ollamaResp.EvalCount,
		},
	}, nil
}

// ChatStream initiates a streaming chat completion with Ollama.
func (o *OllamaProvider) ChatStream(ctx context.Context, req *ChatRequest) (<-chan Chunk, error) {
	messages := make([]ollamaChatMessage, len(req.Messages))
	for i, m := range req.Messages {
		messages[i] = ollamaChatMessage{
			Role:    m.Role,
			Content: m.Content,
		}
	}

	ollamaReq := ollamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Stream:   true,
	}

	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil {
		ollamaReq.Options = make(map[string]any)
		if req.Temperature != nil {
			ollamaReq.Options["temperature"] = *req.Temperature
		}
		if req.TopP != nil {
			ollamaReq.Options["top_p"] = *req.TopP
		}
		if req.MaxTokens != nil {
			ollamaReq.Options["num_predict"] = *req.MaxTokens
		}
	}

	bodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal request: %v", ErrInvalidRequest, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/chat", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Use transport directly or client with context so streaming is not cut off by client.Timeout
	transport := o.httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	httpResp, err := transport.RoundTrip(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderConnection, err)
	}

	if httpResp.StatusCode != http.StatusOK {
		defer httpResp.Body.Close()
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama returned error status %d: %s", httpResp.StatusCode, string(respBody))
	}

	ch := make(chan Chunk, 16)
	id := generateID("chatcmpl-ollama-")

	go func() {
		defer httpResp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(httpResp.Body)
		// Set a reasonably large buffer for long tokens if needed
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		isFirst := true

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Bytes()
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}

			var streamItem ollamaChatResponse
			if err := json.Unmarshal(line, &streamItem); err != nil {
				select {
				case <-ctx.Done():
				case ch <- Chunk{Err: fmt.Errorf("failed to parse stream item: %w", err)}:
				}
				return
			}

			if streamItem.Error != "" {
				select {
				case <-ctx.Done():
				case ch <- Chunk{Err: fmt.Errorf("ollama stream error: %s", streamItem.Error)}:
				}
				return
			}

			now := streamItem.CreatedAt.Unix()
			if now <= 0 {
				now = time.Now().Unix()
			}

			var finishReason *string
			if streamItem.Done {
				fr := "stop"
				if streamItem.DoneReason != "" {
					fr = streamItem.DoneReason
				}
				finishReason = &fr
			}

			deltaRole := ""
			if isFirst && streamItem.Message.Role != "" {
				deltaRole = streamItem.Message.Role
				isFirst = false
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
							Role:    deltaRole,
							Content: streamItem.Message.Content,
						},
						FinishReason: finishReason,
					},
				},
			}

			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}

		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case ch <- Chunk{Err: fmt.Errorf("scanner error reading ollama stream: %w", err)}:
			}
		}
	}()

	return ch, nil
}

// Embeddings generates vector embeddings via Ollama's /api/embed endpoint.
func (o *OllamaProvider) Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: req.Model,
		Input: req.Input,
	}

	bodyBytes, err := json.Marshal(ollamaReq)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to marshal embed request: %v", ErrInvalidRequest, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/embed", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := o.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderConnection, err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("ollama embed returned error status %d: %s", httpResp.StatusCode, string(respBody))
	}

	var ollamaResp ollamaEmbedResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode embed response: %w", err)
	}

	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("ollama embed error: %s", ollamaResp.Error)
	}

	data := make([]EmbeddingData, len(ollamaResp.Embeddings))
	for i, emb := range ollamaResp.Embeddings {
		data[i] = EmbeddingData{
			Index:     i,
			Object:    "embedding",
			Embedding: emb,
		}
	}

	return &EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  req.Model,
		Usage: Usage{
			PromptTokens: len(req.Input) * 5,
			TotalTokens:  len(req.Input) * 5,
		},
	}, nil
}
