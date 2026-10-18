package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"

	"github.com/sauravdx007/rate-limit-service/internal/httpapi"
	"github.com/sauravdx007/rate-limit-service/internal/redisstore"
	"github.com/sauravdx007/rate-limit-service/internal/rules"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	var (
		redisAddr = env("REDIS_ADDR", "localhost:6379")
		port      = env("PORT", "8080")
		failOpen  = env("FAIL_OPEN", "true") == "true"
		instance  = env("INSTANCE_ID", "local")
	)

	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 1 * time.Second,
		PoolSize:     50,
	})
	defer rdb.Close()

	api := &httpapi.API{
		Limiter:  redisstore.New(rdb),
		Rules:    rules.NewStore(rdb),
		Logger:   logger,
		FailOpen: failOpen,
	}

	mux := http.NewServeMux()
	mux.Handle("/", api.Routes())
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		if err := rdb.Ping(ctx).Err(); err != nil {
			http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok\n"))
	})
	// Instance identity, so a cross-instance test can prove requests really
	// did land on different processes.
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(instance + "\n"))
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		logger.Info("starting", "port", port, "redis", redisAddr,
			"fail_open", failOpen, "instance", instance)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: stop accepting new connections, let in-flight
	// requests finish. Without this, a rolling deploy drops live decisions.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("shutdown failed", "err", err)
	}
}
