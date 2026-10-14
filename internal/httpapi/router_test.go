package httpapi_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
)

const postgresImage = "postgres:18.6-alpine3.24"

var (
	pgOnce sync.Once
	pgCtr  *tcpostgres.PostgresContainer
	pgURL  string
	pgErr  error
)

// sharedPostgres starts one container for the whole test binary.
func sharedPostgres(t *testing.T) string {
	t.Helper()
	pgOnce.Do(func() {
		ctx := context.Background()
		pgCtr, pgErr = tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("collection"),
			tcpostgres.WithUsername("collection"),
			tcpostgres.WithPassword("test"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).WithStartupTimeout(90*time.Second)),
		)
		if pgErr == nil {
			pgURL, pgErr = pgCtr.ConnectionString(ctx, "sslmode=disable")
		}
	})
	if pgErr != nil {
		t.Fatalf("start postgres container: %v", pgErr)
	}
	return pgURL
}

func TestMain(m *testing.M) {
	code := m.Run()
	if pgCtr != nil {
		_ = testcontainers.TerminateContainer(pgCtr)
	}
	os.Exit(code)
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func newRouter(t *testing.T, d httpapi.Deps) http.Handler {
	t.Helper()
	h, err := httpapi.NewRouter(d)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return h
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func get(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec.Code, rec.Body.String()
}

func TestHealthz(t *testing.T) {
	// healthz must not depend on DB or storage.
	h := newRouter(t, httpapi.Deps{
		DB:      fakePinger{err: errors.New("down")},
		DataDir: "/nonexistent",
		Logger:  quietLogger(),
	})
	code, _ := get(t, h, http.MethodGet, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestReadyz_OKWithRealPostgres(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), sharedPostgres(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	h := newRouter(t, httpapi.Deps{DB: pool, DataDir: t.TempDir(), Logger: quietLogger()})
	code, body := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%q, want 200", code, body)
	}
}

func TestReadyz_DatabaseDown(t *testing.T) {
	dir := t.TempDir()
	h := newRouter(t, httpapi.Deps{
		DB:      fakePinger{err: errors.New(`pq: password authentication failed for user "collection" SQLSTATE 28P01`)},
		DataDir: dir,
		Logger:  quietLogger(),
	})
	code, body := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	assertOpaque(t, body, dir)
}

func TestReadyz_StoppedPostgresContainer(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("collection"),
		tcpostgres.WithUsername("collection"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(ctr) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	h := newRouter(t, httpapi.Deps{DB: pool, DataDir: dir, Logger: quietLogger()})
	if code, body := get(t, h, http.MethodGet, "/readyz"); code != http.StatusOK {
		t.Fatalf("before stop: status = %d body=%q, want 200", code, body)
	}

	stopTimeout := 5 * time.Second
	if err := ctr.Stop(ctx, &stopTimeout); err != nil {
		t.Fatal(err)
	}
	code, body := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("after stop: status = %d, want 503", code)
	}
	assertOpaque(t, body, dir)
}

func TestReadyz_DataDirMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist")
	h := newRouter(t, httpapi.Deps{DB: fakePinger{}, DataDir: missing, Logger: quietLogger()})
	code, body := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	assertOpaque(t, body, missing)
}

func TestReadyz_DataDirUnwritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	h := newRouter(t, httpapi.Deps{DB: fakePinger{}, DataDir: dir, Logger: quietLogger()})
	code, body := get(t, h, http.MethodGet, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	assertOpaque(t, body, dir)
}

func TestReadyz_LeavesNoProbeFilesBehind(t *testing.T) {
	dir := t.TempDir()
	h := newRouter(t, httpapi.Deps{DB: fakePinger{}, DataDir: dir, Logger: quietLogger()})
	if code, _ := get(t, h, http.MethodGet, "/readyz"); code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("data dir not clean after probe: %v", entries)
	}
}

// assertOpaque checks a 503 body leaks neither the data dir nor driver/SQL text.
func assertOpaque(t *testing.T, body, dir string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, bad := range []string{dir, "sqlstate", "pq:", "pgx", "password", "authentication", "dial", "connection refused", "no such file", "permission denied", "select ", "/"} {
		if strings.Contains(lower, strings.ToLower(bad)) {
			t.Errorf("503 body %q leaks %q", body, bad)
		}
	}
	if strings.TrimSpace(body) == "" {
		t.Error("503 body is empty")
	}
}
