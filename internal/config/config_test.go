package config_test

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/config"
)

const validDB = "postgres://db:5432/collection?sslmode=disable"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_Defaults(t *testing.T) {
	dir := t.TempDir()
	c, err := config.Load(env(map[string]string{"DATABASE_URL": validDB, "DATA_DIR": dir}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.AppEnv != "development" {
		t.Errorf("AppEnv = %q", c.AppEnv)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q", c.HTTPAddr)
	}
	if c.DatabaseURL != validDB {
		t.Errorf("DatabaseURL = %q", c.DatabaseURL)
	}
	if c.DBMaxConns != 10 {
		t.Errorf("DBMaxConns = %d", c.DBMaxConns)
	}
	if c.DataDir != dir {
		t.Errorf("DataDir = %q", c.DataDir)
	}
	if c.WorkspaceName != "Our Collection" {
		t.Errorf("WorkspaceName = %q", c.WorkspaceName)
	}
	if c.TMDBAPIKey != "" {
		t.Errorf("TMDBAPIKey = %q, want empty", c.TMDBAPIKey)
	}
	if c.TMDBMockFixture {
		t.Error("TMDBMockFixture = true, want false by default")
	}
	if c.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v", c.LogLevel)
	}
	if c.LogFormat != "json" {
		t.Errorf("LogFormat = %q", c.LogFormat)
	}
}

func TestLoad_Overrides(t *testing.T) {
	dir := t.TempDir()
	c, err := config.Load(env(map[string]string{
		"APP_ENV":        "production",
		"HTTP_ADDR":      "127.0.0.1:9000",
		"DATABASE_URL":   "postgresql://localhost/db",
		"DB_MAX_CONNS":   "25",
		"DATA_DIR":       dir,
		"WORKSPACE_NAME": "Fede & Co",
		"TMDB_API_KEY":   "  secret-key  ",
		"LOG_LEVEL":      "debug",
		"LOG_FORMAT":     "text",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.AppEnv != "production" || c.HTTPAddr != "127.0.0.1:9000" || c.DBMaxConns != 25 ||
		c.WorkspaceName != "Fede & Co" || c.TMDBAPIKey != "secret-key" || c.LogLevel != slog.LevelDebug || c.LogFormat != "text" {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestLoad_TMDBMockFixture(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"DATABASE_URL":      validDB,
		"DATA_DIR":          t.TempDir(),
		"TMDB_MOCK_FIXTURE": "true",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !c.TMDBMockFixture {
		t.Fatal("TMDBMockFixture = false, want true")
	}
}

func TestLoad_Validation(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // empty = must succeed
	}{
		{"missing database url", map[string]string{"DATABASE_URL": ""}, "DATABASE_URL is required"},
		{"blank database url", map[string]string{"DATABASE_URL": "   "}, "DATABASE_URL is required"},
		{"mysql scheme", map[string]string{"DATABASE_URL": "mysql://h/db"}, "DATABASE_URL must be a postgres"},
		{"http scheme", map[string]string{"DATABASE_URL": "http://h/db"}, "DATABASE_URL must be a postgres"},
		{"no scheme", map[string]string{"DATABASE_URL": "just-a-string"}, "DATABASE_URL must be a postgres"},
		{"no host", map[string]string{"DATABASE_URL": "postgres:///db"}, "DATABASE_URL must be a postgres"},
		{"postgresql scheme ok", map[string]string{"DATABASE_URL": "postgresql://h/db"}, ""},

		{"max conns zero", map[string]string{"DB_MAX_CONNS": "0"}, "DB_MAX_CONNS"},
		{"max conns negative", map[string]string{"DB_MAX_CONNS": "-3"}, "DB_MAX_CONNS"},
		{"max conns 101", map[string]string{"DB_MAX_CONNS": "101"}, "DB_MAX_CONNS"},
		{"max conns huge", map[string]string{"DB_MAX_CONNS": "99999999999999"}, "DB_MAX_CONNS"},
		{"max conns non numeric", map[string]string{"DB_MAX_CONNS": "abc"}, "DB_MAX_CONNS"},
		{"max conns float", map[string]string{"DB_MAX_CONNS": "5.5"}, "DB_MAX_CONNS"},
		{"max conns 1 ok", map[string]string{"DB_MAX_CONNS": "1"}, ""},
		{"max conns 100 ok", map[string]string{"DB_MAX_CONNS": "100"}, ""},

		{"bad app env", map[string]string{"APP_ENV": "staging"}, "APP_ENV"},
		{"app env wrong case", map[string]string{"APP_ENV": "Production"}, "APP_ENV"},
		{"invalid TMDB fixture flag", map[string]string{"TMDB_MOCK_FIXTURE": "sometimes"}, "TMDB_MOCK_FIXTURE"},
		{"TMDB fixture forbidden in production", map[string]string{"APP_ENV": "production", "TMDB_MOCK_FIXTURE": "true"}, "TMDB_MOCK_FIXTURE cannot be enabled"},
		{"bad log level", map[string]string{"LOG_LEVEL": "verbose"}, "LOG_LEVEL"},
		{"bad log format", map[string]string{"LOG_FORMAT": "xml"}, "LOG_FORMAT"},
		{"http addr no port", map[string]string{"HTTP_ADDR": "localhost"}, "HTTP_ADDR"},
		{"http addr port only", map[string]string{"HTTP_ADDR": "8080"}, "HTTP_ADDR"},
		{"http addr host:port ok", map[string]string{"HTTP_ADDR": "0.0.0.0:8080"}, ""},
		{"nonexistent data dir", map[string]string{"DATA_DIR": "/nonexistent/definitely/not/here"}, "DATA_DIR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{"DATABASE_URL": validDB, "DATA_DIR": dir}
			for k, v := range tt.env {
				m[k] = v
			}
			_, err := config.Load(env(m))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoad_DataDirIsAFile(t *testing.T) {
	f := t.TempDir() + "/file"
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(env(map[string]string{"DATABASE_URL": validDB, "DATA_DIR": f}))
	if err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
		t.Fatalf("expected DATA_DIR error, got %v", err)
	}
}

func TestLoad_ReportsAllErrorsTogether(t *testing.T) {
	_, err := config.Load(env(map[string]string{
		"APP_ENV":      "staging",
		"HTTP_ADDR":    "nope",
		"DB_MAX_CONNS": "0",
		"LOG_LEVEL":    "loud",
		"LOG_FORMAT":   "yaml",
		"DATA_DIR":     "/nonexistent/definitely/not/here",
	}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"APP_ENV", "HTTP_ADDR", "DATABASE_URL", "DB_MAX_CONNS", "LOG_LEVEL", "LOG_FORMAT", "DATA_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %s: %v", want, err)
		}
	}
}

func TestLoad_ErrorNeverLeaksDatabasePassword(t *testing.T) {
	const secret = "S3cr3tPassw0rd"
	urls := []string{
		"mysql://collection:" + secret + "@db:3306/x",
		"postgres://collection:" + secret + "@/x",
		"postgres://collection:" + secret + "@ho st/x",
		"http://collection:" + secret + "@db/x",
		secret,
		"postgres://collection:" + secret + "@db:notaport/x",
	}
	for _, u := range urls {
		// Also make every other setting invalid so all error paths run.
		_, err := config.Load(env(map[string]string{
			"DATABASE_URL": u,
			"DB_MAX_CONNS": "x",
			"APP_ENV":      "bad",
			"DATA_DIR":     "/nonexistent/definitely/not/here",
		}))
		if err == nil {
			t.Fatalf("%q: expected error", u)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%q: error leaks password: %v", u, err)
		}
	}

	// A valid URL with a password must not leak either when other settings fail.
	_, err := config.Load(env(map[string]string{
		"DATABASE_URL": "postgres://collection:" + secret + "@db:5432/x",
		"APP_ENV":      "bad",
		"DATA_DIR":     t.TempDir(),
	}))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("unexpected error state: %v", err)
	}
}
