// Command api is the Collection App backend. This file only wires things together.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/hardcover"
	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/openlibrary"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/config"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
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
	settingsSvc := workspace.NewService(workspace.NewPostgres(pool))
	itemSvc := httpapi.NewItems(pool, clock.System{}, settingsSvc)
	imageSvc := images.NewService(images.NewPostgres(pool), store, itemSvc, clock.System{}, logger)
	go imageSvc.RunSweeper(scope.With(ctx, workspaceID), images.SweepInterval) // stops when ctx is cancelled on shutdown
	trashSvc := trash.New(pool, store, clock.System{}, logger, workspaceID)
	go trashSvc.RunAutoPurge(ctx, 24*time.Hour) // stops when ctx is cancelled on shutdown
	tmdbClient := tmdb.NewWithOptions(cfg.TMDBAPIKey, tmdb.Options{MockFixture: cfg.TMDBMockFixture})
	lookupStore := lookup.NewStore(pool, store, clock.System{}, itemSvc, logger, workspaceID)
	tmdbProvider := lookup.NewTMDBProvider(tmdbClient)
	// Books use Open Library: no key, on by default. The test-stack fixture flag also serves its one fixture book (D-096).
	bookProvider := lookup.NewOpenLibraryProvider(openlibrary.NewWithOptions(openlibrary.Options{
		MockFixture: cfg.TMDBMockFixture, Contact: cfg.OpenLibraryContact,
	}))
	lookupSvc := lookup.New(lookup.Providers{types.Movie: tmdbProvider, types.TVShow: tmdbProvider, types.Book: bookProvider}, lookupStore, itemSvc, settingsSvc, clock.System{}, workspaceID).
		WithHardcover(hardcover.New(cfg.HardcoverAPIKey, cfg.OpenLibraryContact)) // optional gap-filler: disabled without a key
	if _, err := lookupStore.Maintain(ctx, lookupSvc.RefreshOwned); err != nil {
		logger.Warn("lookup retention maintenance failed at startup", "err", err)
	}
	go lookupStore.Run(ctx, 24*time.Hour, lookupSvc.RefreshOwned) // stops when ctx is cancelled on shutdown

	handler, err := httpapi.NewRouter(httpapi.Deps{
		DB:                  pool,
		DataDir:             cfg.DataDir,
		Logger:              logger,
		Clock:               clock.System{},
		WorkspaceID:         workspaceID,
		Settings:            settingsSvc,
		TMDB:                tmdbProvider,
		Books:               bookProvider,
		Lookup:              lookupSvc,
		LookupAvailable:     tmdbProvider.Enabled(),
		BookLookupAvailable: bookProvider.Enabled(),
		Items:               itemSvc,
		Search:              search.New(pool, workspaceID),
		Images:              imageSvc,
		Trash:               trashSvc,
	})
	if err != nil {
		return err
	}

	// ReadTimeout/WriteTimeout are the defaults; the request guard extends both
	// for multipart image uploads (see internal/httpapi/guard.go).
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.AppEnv)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
	}
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}

// healthcheck probes /readyz on the local listener. The distroless runtime image has
// no shell or curl, so compose calls `/app/api healthcheck`.
func healthcheck() int {
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort("127.0.0.1", port)+"/readyz", nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
