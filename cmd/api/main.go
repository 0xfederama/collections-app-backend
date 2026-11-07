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
	"strings"
	"syscall"
	"time"

	"github.com/0xfederama/collections-app-backend/db"
	"github.com/0xfederama/collections-app-backend/internal/favicons"
	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/integrations/hardcover"
	"github.com/0xfederama/collections-app-backend/internal/integrations/openlibrary"
	"github.com/0xfederama/collections-app-backend/internal/integrations/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/config"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/logs"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		os.Exit(migrateUp())
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
	// The log buffer exists only when the viewer is on: otherwise nothing is recorded.
	var logBuf *logs.Buffer
	if cfg.LogsUI {
		logBuf = logs.NewBuffer(logs.DefaultSize)
	}
	logger := newLogger(cfg, logBuf)
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
	trashSvc := trash.NewService(trash.NewPostgres(pool), store, clock.System{}, logger)
	go trashSvc.RunAutoPurge(scope.With(ctx, workspaceID), 24*time.Hour) // stops when ctx is cancelled on shutdown
	tmdbClient := tmdb.NewWithOptions(cfg.TMDBAPIKey, tmdb.Options{MockFixture: cfg.TMDBMockFixture})
	lookupStore := lookup.NewStore(pool, store, clock.System{}, itemSvc, logger, workspaceID)
	tmdbProvider := lookup.NewTMDBProvider(tmdbClient)
	// Books use Open Library: no key, on by default. The test-stack fixture flag also serves its one fixture book (D-096).
	bookProvider := lookup.NewOpenLibraryProvider(openlibrary.NewWithOptions(openlibrary.Options{
		MockFixture: cfg.TMDBMockFixture, Contact: cfg.OpenLibraryContact,
	}))
	lookupSvc := lookup.New(lookup.Providers{types.Movie: tmdbProvider, types.TVShow: tmdbProvider, types.Book: bookProvider}, lookupStore, itemSvc, settingsSvc, clock.System{}, workspaceID).
		WithHardcover(hardcover.New(cfg.HardcoverAPIKey, cfg.OpenLibraryContact)) // optional gap-filler: disabled without a key
	if _, err := lookupStore.Maintain(ctx, lookupSvc.RefreshScheduled); err != nil {
		logger.Warn("lookup weekly refresh failed at startup", "err", err)
	}
	go lookupStore.Run(ctx, lookup.MaintenanceInterval, lookupSvc.RefreshScheduled) // stops when ctx is cancelled on shutdown

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
		Search:              search.NewService(search.NewPostgres(pool)),
		Images:              imageSvc,
		Trash:               trashSvc,
		Favicons:            favicons.NewService(favicons.NewHTTPFetcher(favicons.Options{}), store, clock.System{}, logger),
		Logs:                logBuf,
	})
	if err != nil {
		return err
	}

	// ReadTimeout/WriteTimeout are the defaults; multipart image uploads extend both
	// per request (see internal/images/handler.go uploadGuard).
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

// newLogger writes to stderr in the configured format. With a buffer (LOGS_UI) every
// record is also kept for the log viewer.
func newLogger(cfg config.Config, buf *logs.Buffer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	if buf != nil {
		h = logs.NewHandler(h, buf)
	}
	return slog.New(h)
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

// migrateUp is `api migrate`: apply pending migrations to DATABASE_URL and exit.
// It needs only DATABASE_URL; the server itself never migrates at startup.
func migrateUp() int {
	url := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if url == "" {
		fmt.Fprintln(os.Stderr, "migrate: DATABASE_URL is required")
		return 1
	}
	v, err := database.Migrate(db.Migrations(), "migrations", url)
	if errors.Is(err, database.ErrDirty) {
		fmt.Fprintln(os.Stderr, "migrate: refusing to continue:", err)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		return 1
	}
	fmt.Printf("migrations applied, schema version %d\n", v)
	return 0
}
