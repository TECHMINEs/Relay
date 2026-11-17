// Package main is the entrypoint for the Relay gateway server.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TECHMINEs/Relay/internal/api"
	"github.com/TECHMINEs/Relay/internal/breaker"
	"github.com/TECHMINEs/Relay/internal/config"
	"github.com/TECHMINEs/Relay/internal/metrics"
	"github.com/TECHMINEs/Relay/internal/provider"
	"github.com/TECHMINEs/Relay/internal/ratelimit"
	"github.com/TECHMINEs/Relay/internal/router"
	"github.com/TECHMINEs/Relay/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m, err := metrics.New(reg)
	if err != nil {
		log.Fatalf("failed to create metrics: %v", err)
	}

	bCfg := breaker.Config{
		WindowSize:        cfg.BreakerWindowSize,
		FailureThreshold:  cfg.BreakerFailureThreshold,
		MinRequests:       cfg.BreakerMinRequests,
		Cooldown:          cfg.BreakerCooldown,
		MaxHalfOpenProbes: cfg.BreakerMaxHalfOpenProbes,
	}

	retryCfg := router.RetryConfig{
		MaxAttempts:    cfg.RetryMaxAttempts,
		AttemptTimeout: cfg.RetryAttemptTimeout,
		BaseDelay:      cfg.RetryBaseDelay,
		MaxDelay:       cfg.RetryMaxDelay,
	}

	entries := buildProviderEntries(cfg, bCfg)

	rt, err := router.New(entries, retryCfg)
	if err != nil {
		log.Fatalf("failed to build router: %v", err)
	}

	go pollBreakerStates(entries, m)

	var (
		pgStore     *store.Store
		asyncLogger *metrics.AsyncLogger
	)
	if cfg.DatabaseURL != "" {
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		pool, err := store.NewPool(dbCtx, cfg.DatabaseURL)
		dbCancel()
		if err != nil {
			log.Printf("warning: postgres connection failed (%v); running in degraded auth/log mode", err)
		} else {
			defer pool.Close()
			pgStore = store.New(pool)
			asyncLogger = metrics.NewAsyncLogger(pgStore, 1000, 50, 2*time.Second).WithDropCounter(m.DroppedLogEntries)
			asyncLogger.Start(context.Background())
			defer asyncLogger.Stop()
			log.Printf("Postgres store and async logger connected")
		}
	}

	var limiter *ratelimit.Limiter
	if cfg.RedisURL != "" {
		opt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Printf("warning: invalid REDIS_URL (%v); skipping rate limiter", err)
		} else {
			rdb := redis.NewClient(opt)
			defer rdb.Close()
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := rdb.Ping(pingCtx).Err(); err != nil {
				pingCancel()
				log.Printf("warning: redis ping failed (%v); running without rate limiter", err)
			} else {
				pingCancel()
				lCtx, lCancel := context.WithTimeout(context.Background(), 5*time.Second)
				limiter, err = ratelimit.New(lCtx, rdb)
				lCancel()
				if err != nil {
					log.Printf("warning: failed to preload rate limiter lua script: %v", err)
				} else {
					log.Printf("Redis rate limiter connected and initialized")
				}
			}
		}
	}

	chiRouter := chi.NewRouter()

	var handlerOpts []api.HandlerOption
	if pgStore != nil {
		handlerOpts = append(handlerOpts, api.WithAuth(pgStore))
	}
	if limiter != nil {
		handlerOpts = append(handlerOpts, api.WithRateLimiter(limiter))
	}
	if asyncLogger != nil {
		handlerOpts = append(handlerOpts, api.WithAsyncLogger(asyncLogger))
	}

	handler := api.NewHandlerWithMetrics(rt, m, handlerOpts...)
	api.RegisterRoutes(chiRouter, handler, reg)

	addr := fmt.Sprintf(":%d", cfg.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      chiRouter,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Relay LLM gateway listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-stopChan
	log.Println("Shutting down Relay gateway gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Graceful shutdown failed: %v", err)
	}

	log.Println("Relay gateway stopped cleanly")
}

// buildProviderEntries wires up the provider selected by DEFAULT_PROVIDER.
func buildProviderEntries(cfg *config.Config, bCfg breaker.Config) []router.ProviderEntry {
	switch cfg.DefaultProvider {
	case "ollama":
		p := provider.NewOllamaProvider(cfg)
		log.Printf("Registered Ollama provider targeting %s", cfg.OllamaBaseURL)
		return []router.ProviderEntry{
			{Provider: p, Weight: 100, Breaker: breaker.New(bCfg)},
		}
	case "mock":
		p := provider.NewMockProvider(cfg)
		log.Printf("Registered Mock provider (error_rate=%.2f stall=%v)", cfg.MockErrorRate, cfg.MockStallForever)
		return []router.ProviderEntry{
			{Provider: p, Weight: 100, Breaker: breaker.New(bCfg)},
		}
	default:
		log.Fatalf("unsupported DEFAULT_PROVIDER: %s", cfg.DefaultProvider)
		return nil
	}
}

// pollBreakerStates publishes each breaker's state to Prometheus every 5s.
func pollBreakerStates(entries []router.ProviderEntry, m *metrics.Metrics) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		for _, e := range entries {
			var val float64
			switch e.Breaker.State() {
			case breaker.StateClosed:
				val = 0
			case breaker.StateHalfOpen:
				val = 1
			case breaker.StateOpen:
				val = 2
			}
			m.CircuitBreakerState.WithLabelValues(e.Provider.Name()).Set(val)
		}
	}
}
