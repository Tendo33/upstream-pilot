package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Tendo33/upstream-pilot/internal/app"
	"github.com/Tendo33/upstream-pilot/internal/config"
	"github.com/Tendo33/upstream-pilot/internal/database"
	"github.com/Tendo33/upstream-pilot/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	migrateOnly := flag.Bool("migrate-only", false, "apply database migrations and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("Upstream Pilot %s (%s, %s)\n", version.Version, version.Commit, version.BuildTime)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("configuration failed", slog.Any("error", err))
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("database connection failed", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()
	if cfg.AutoMigrate || *migrateOnly {
		if err := database.Migrate(ctx, pool); err != nil {
			logger.Error("database migration failed", slog.Any("error", err))
			os.Exit(1)
		}
	}
	application, err := app.New(cfg, pool, logger)
	if err != nil {
		logger.Error("application initialization failed", slog.Any("error", err))
		os.Exit(1)
	}
	if *migrateOnly {
		logger.Info("database and audit log migrations applied")
		return
	}
	scheduler := application.NewScheduler()
	go scheduler.Run(ctx)

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           application.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server listening", slog.String("address", cfg.ListenAddr), slog.String("version", version.Version))
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server stopped unexpectedly", slog.Any("error", err))
			os.Exit(1)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.Any("error", err))
	}
}
