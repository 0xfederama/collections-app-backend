package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xfederama/collections-app-backend/internal/config"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// stripImages is the `strip-images` subcommand: it removes the metadata from the
// originals stored before uploads were stripped (images.Backfill) and prints the
// counts. The exit code is non-zero only when an I/O or database error occurred.
func stripImages() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	workspaceID, err := workspace.Bootstrap(ctx, pool, cfg.WorkspaceName)
	if err != nil {
		return err
	}
	store, err := storage.NewLocal(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	res, err := images.Backfill(ctx, pool, store, logger, workspaceID)
	fmt.Printf("total: %d\nstripped: %d\nalready clean: %d\nskipped: %d\nfailed: %d\n",
		res.Total, res.Stripped, res.AlreadyClean, res.Skipped, res.Failed)
	if err != nil {
		return err
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d attachments failed, see the log", res.Failed)
	}
	return nil
}
