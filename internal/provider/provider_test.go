package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TECHMINEs/Relay/internal/config"
)

func TestMockProvider(t *testing.T) {
	t.Run("Chat returns expected response schema", func(t *testing.T) {
		cfg := &config.Config{
			MockLatencyMin:   0,
			MockLatencyMax:   0,
			MockErrorRate:    0,
			MockStallForever: false,
		}
		p := NewMockProvider(cfg)
		req := &ChatRequest{
			Model: "test-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Hello"},
			},
		}

		resp, err := p.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Object != "chat.completion" {
			t.Errorf("expected object chat.completion, got %s", resp.Object)
		}
		if len(resp.Choices) != 1 {
			t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
		}
		if resp.Choices[0].Message.Role != "assistant" {
			t.Errorf("expected role assistant, got %s", resp.Choices[0].Message.Role)
		}
		if resp.Choices[0].FinishReason != "stop" {
			t.Errorf("expected finish_reason stop, got %s", resp.Choices[0].FinishReason)
		}
	})

	t.Run("ChatStream streams chunks in correct order", func(t *testing.T) {
		cfg := &config.Config{
			MockChunkDelay: 1 * time.Millisecond,
		}
		p := NewMockProvider(cfg)
		req := &ChatRequest{
			Model: "test-model",
			Messages: []ChatMessage{
				{Role: "user", Content: "Hello"},
			},
		}

		ch, err := p.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var chunks []Chunk
		for chunk := range ch {
			chunks = append(chunks, chunk)
		}

		if len(chunks) < 2 {
			t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
		}

		// First chunk must have role assistant
		if chunks[0].Choices[0].Delta.Role != "assistant" {
			t.Errorf("first chunk must have role assistant, got %s", chunks[0].Choices[0].Delta.Role)
		}

		// Last chunk must have finish_reason stop
		lastChunk := chunks[len(chunks)-1]
		if lastChunk.Choices[0].FinishReason == nil || *lastChunk.Choices[0].FinishReason != "stop" {
			t.Errorf("last chunk must have finish_reason stop")
		}

		var combined string
		for _, c := range chunks {
			combined += c.Choices[0].Delta.Content
		}
		expected := "Hello from Relay mock provider streaming!"
		if combined != expected {
			t.Errorf("expected text %q, got %q", expected, combined)
		}
	})

	t.Run("Stall forever is cancelled by context", func(t *testing.T) {
		cfg := &config.Config{
			MockStallForever: true,
		}
		p := NewMockProvider(cfg)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		req := &ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
		_, err := p.Chat(ctx, req)
		if err == nil {
			t.Fatal("expected context error, got nil")
		}
		if p.CancelCount() != 1 {
			t.Errorf("expected cancel count 1, got %d", p.CancelCount())
		}
	})

	t.Run("Simulated error rate triggers error", func(t *testing.T) {
		cfg := &config.Config{
			MockErrorRate: 1.0, // 100% error rate
		}
		p := NewMockProvider(cfg)
		req := &ChatRequest{Model: "gpt-4", Messages: []ChatMessage{{Role: "user", Content: "hi"}}}
		_, err := p.Chat(context.Background(), req)
		if err != ErrSimulatedFailure {
			t.Errorf("expected ErrSimulatedFailure, got %v", err)
		}
	})

	t.Run("Embeddings works as expected", func(t *testing.T) {
		cfg := &config.Config{}
		p := NewMockProvider(cfg)
		req := &EmbeddingRequest{Model: "text-embed", Input: []string{"a", "b"}}
		resp, err := p.Embeddings(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Data) != 2 {
			t.Errorf("expected 2 embedding results, got %d", len(resp.Data))
		}
	})
}

func TestOllamaProvider(t *testing.T) {
	t.Run("Chat parses ollama response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/chat" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			resp := ollamaChatResponse{
				Model: "llama3",
				Message: ollamaChatMessage{
					Role:    "assistant",
					Content: "Hello from Ollama!",
				},
				Done:            true,
				DoneReason:      "stop",
				PromptEvalCount: 15,
				EvalCount:       25,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		cfg := &config.Config{
			OllamaBaseURL: server.URL,
			OllamaTimeout: 5 * time.Second,
		}
		provider := NewOllamaProvider(cfg)

		req := &ChatRequest{
			Model: "llama3",
			Messages: []ChatMessage{
				{Role: "user", Content: "Hello"},
			},
		}

		resp, err := provider.Chat(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if resp.Choices[0].Message.Content != "Hello from Ollama!" {
			t.Errorf("unexpected message content: %s", resp.Choices[0].Message.Content)
		}
		if resp.Usage.PromptTokens != 15 || resp.Usage.CompletionTokens != 25 {
			t.Errorf("unexpected usage: %+v", resp.Usage)
		}
	})

	t.Run("ChatStream parses streaming response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("expected flusher")
			}

			items := []ollamaChatResponse{
				{
					Model:   "llama3",
					Message: ollamaChatMessage{Role: "assistant", Content: "Part1"},
					Done:    false,
				},
				{
					Model:      "llama3",
					Message:    ollamaChatMessage{Content: "Part2"},
					Done:       true,
					DoneReason: "stop",
				},
			}

			for _, item := range items {
				data, _ := json.Marshal(item)
				w.Write(data)
				w.Write([]byte("\n"))
				flusher.Flush()
			}
		}))
		defer server.Close()

		cfg := &config.Config{
			OllamaBaseURL: server.URL,
			OllamaTimeout: 5 * time.Second,
		}
		provider := NewOllamaProvider(cfg)

		req := &ChatRequest{
			Model: "llama3",
			Messages: []ChatMessage{
				{Role: "user", Content: "Hi"},
			},
			Stream: true,
		}

		ch, err := provider.ChatStream(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var collected string
		var finalFinishReason *string
		for chunk := range ch {
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
			collected += chunk.Choices[0].Delta.Content
			if chunk.Choices[0].FinishReason != nil {
				finalFinishReason = chunk.Choices[0].FinishReason
			}
		}

		if collected != "Part1Part2" {
			t.Errorf("expected Part1Part2, got %s", collected)
		}
		if finalFinishReason == nil || *finalFinishReason != "stop" {
			t.Errorf("expected finish_reason stop")
		}
	})

	t.Run("Embeddings parses ollama embed response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/embed" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			resp := ollamaEmbedResponse{
				Model: "nomic-embed-text",
				Embeddings: [][]float64{
					{0.1, 0.2, 0.3},
					{0.4, 0.5, 0.6},
				},
			}
			json.NewEncoder(w).Encode(resp)
		}))
		defer server.Close()

		cfg := &config.Config{
			OllamaBaseURL: server.URL,
			OllamaTimeout: 5 * time.Second,
		}
		provider := NewOllamaProvider(cfg)

		req := &EmbeddingRequest{
			Model: "nomic-embed-text",
			Input: []string{"first input", "second input"},
		}

		resp, err := provider.Embeddings(context.Background(), req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(resp.Data) != 2 {
			t.Fatalf("expected 2 embeddings, got %d", len(resp.Data))
		}
		if len(resp.Data[0].Embedding) != 3 {
			t.Errorf("expected 3 dims, got %d", len(resp.Data[0].Embedding))
		}
	})
}
