package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/vlgrigoriev/coeus/internal/app"
	"github.com/vlgrigoriev/coeus/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	application, err := app.Build(ctx, cfg)
	if err != nil {
		slog.Error("failed to build app", "error", err)
		os.Exit(1)
	}
	defer application.Close()

	slog.Info("coeus started", "addr", cfg.Server.Addr)

	httpServer := &http.Server{
		Addr:         cfg.Server.Addr,
		Handler:      application.Server.Handler(),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
			cancel()
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("forced shutdown", "error", err)
	}
	cancel()
	slog.Info("coeus stopped")
}
