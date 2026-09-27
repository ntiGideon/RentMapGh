// Command web runs the RentMap HTTP server.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"rentmapgh/internal/config"
	"rentmapgh/internal/db"
	"rentmapgh/internal/platform/sms"
	"rentmapgh/internal/platform/storage"
	"rentmapgh/internal/server"
	"rentmapgh/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.MigrateOnBoot {
		if err := db.Migrate(cfg.DatabaseURL); err != nil {
			return err
		}
	}

	connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	database, err := db.Open(connectCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer database.Close()

	smsSender := sms.New(cfg.SMS)
	slog.Info("sms provider", "provider", smsSender.Name())
	media, err := openMedia(ctx, cfg)
	if err != nil {
		return err
	}
	deps := server.Deps{Cfg: cfg, DB: database, Assets: web.NewAssets(cfg.StaticFromDisk), SMS: smsSender,
		Files: storage.Disk{Root: cfg.StorageDir}, Media: media}
	if deps.Listings, err = server.NewListings(deps); err != nil {
		return err
	}
	server.StartJobs(ctx, deps)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(deps),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    64 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Addr, "env", cfg.Env, "base_url", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	return srv.Shutdown(shutdownCtx)
}

// openMedia returns the store for listing photos. An S3 bucket is created
// on first boot if it doesn't exist yet.
func openMedia(ctx context.Context, cfg config.Config) (storage.Store, error) {
	if cfg.MediaStore != "s3" {
		slog.Info("media store", "store", "disk", "dir", cfg.MediaDir)
		return storage.Disk{Root: cfg.MediaDir}, nil
	}
	s3, err := storage.NewS3(cfg.S3)
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s3.EnsureBucket(bctx); err != nil {
		return nil, err
	}
	slog.Info("media store", "store", "s3", "endpoint", cfg.S3.Endpoint, "bucket", cfg.S3.Bucket)
	return s3, nil
}

func setupLogger(cfg config.Config) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.IsDev() {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}
