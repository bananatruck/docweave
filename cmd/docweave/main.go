package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/bananatruck/docweave/internal/config"
	"github.com/bananatruck/docweave/internal/crawler"
	"github.com/bananatruck/docweave/internal/httpapi"
	"github.com/bananatruck/docweave/internal/observability"
	"github.com/bananatruck/docweave/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("docweave stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	database, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer database.Pool.Close()
	if err := store.Migrate(ctx, database.Pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	metrics := observability.New(prometheus.DefaultRegisterer)
	var group sync.WaitGroup
	errorsChannel := make(chan error, cfg.Workers+2)
	if cfg.Role == "worker" || cfg.Role == "all" {
		fetcher := crawler.NewFetcher(cfg.UserAgent, cfg.MaxBody, cfg.AllowPrivate)
		for index := 0; index < cfg.Workers; index++ {
			worker := &crawler.Worker{
				ID:    fmt.Sprintf("%s-%d-%d", hostname(), os.Getpid(), index),
				Store: database, Fetcher: fetcher, Metrics: metrics, LeaseTTL: cfg.LeaseTTL, Logger: logger,
			}
			group.Add(1)
			go func() {
				defer group.Done()
				if err := worker.Run(ctx); err != nil {
					errorsChannel <- err
				}
			}()
		}
	}

	var server *http.Server
	if cfg.Role == "api" || cfg.Role == "all" {
		server = &http.Server{
			Addr: cfg.Addr, Handler: httpapi.New(database, cfg, logger),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		}
	} else {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		server = &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	}
	group.Add(1)
	go func() {
		defer group.Done()
		logger.Info("http server started", "addr", cfg.Addr, "role", cfg.Role)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsChannel <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errorsChannel:
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		group.Wait()
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	group.Wait()
	return nil
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return "worker"
	}
	return name
}
