package config_test

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/platform/config"
)

const validDB = "postgres://db:5432/collection?sslmode=disable"

// env serves m. Unless m names an identity setting, it adds a valid one for the
// environment (Access in production, dev identities otherwise), so tests of other
// settings do not repeat it; the identity tests use rawEnv.
func env(m map[string]string) func(string) string {
	identity := false
	for _, k := range []string{"ACCESS_TEAM_DOMAIN", "ACCESS_AUD", "OWNER_EMAIL", "DEV_IDENTITY"} {
		if _, ok := m[k]; ok {
			identity = true
		}
	}
	if !identity {
		if m["APP_ENV"] == "production" {
			m["ACCESS_TEAM_DOMAIN"], m["ACCESS_AUD"], m["OWNER_EMAIL"] = "team.cloudflareaccess.com", "aud-tag", "owner@example.test"
		} else {
			m["DEV_IDENTITY"] = "true"
		}
	}
	return rawEnv(m)
}

func rawEnv(m map[string]string) func(string) string {
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
		"APP_ENV":      "production",
		"HTTP_ADDR":    "127.0.0.1:9000",
		"DATABASE_URL": "postgresql://localhost/db",
		"DB_MAX_CONNS": "25",
		"DATA_DIR":     dir,
		"TMDB_API_KEY": "  secret-key  ",
		"LOG_LEVEL":    "debug",
		"LOG_FORMAT":   "text",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.AppEnv != "production" || c.HTTPAddr != "127.0.0.1:9000" || c.DBMaxConns != 25 ||
		c.TMDBAPIKey != "secret-key" || c.LogLevel != slog.LevelDebug || c.LogFormat != "text" {
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

func TestLoad_OpenLibraryContact(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"DATABASE_URL":        validDB,
		"DATA_DIR":            t.TempDir(),
		"OPENLIBRARY_CONTACT": "  me@example.test  ",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.OpenLibraryContact != "me@example.test" {
		t.Fatalf("OpenLibraryContact = %q", c.OpenLibraryContact)
	}
}

func TestLoad_HardcoverAPIKeyIsOptional(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"absent", "", ""},
		{"trimmed", "  tok  ", "tok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := config.Load(env(map[string]string{
				"DATABASE_URL": validDB, "DATA_DIR": t.TempDir(), "HARDCOVER_API_KEY": tc.in,
			}))
			if err != nil || c.HardcoverAPIKey != tc.want {
				t.Fatalf("Load() = %q, %v; want %q", c.HardcoverAPIKey, err, tc.want)
			}
		})
	}
}

// TestLoad_LogsUI: the log viewer is on in development and off in production unless
// LOGS_UI says otherwise; an invalid value is a startup error.
func TestLoad_LogsUI(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    bool
		wantErr string
	}{
		{"development default on", map[string]string{"APP_ENV": "development"}, true, ""},
		{"production default off", map[string]string{"APP_ENV": "production"}, false, ""},
		{"production explicit on", map[string]string{"APP_ENV": "production", "LOGS_UI": "true"}, true, ""},
		{"development explicit off", map[string]string{"LOGS_UI": "false"}, false, ""},
		{"blank falls back to default", map[string]string{"APP_ENV": "production", "LOGS_UI": "  "}, false, ""},
		{"invalid value", map[string]string{"LOGS_UI": "sometimes"}, false, "LOGS_UI must be true or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{"DATABASE_URL": validDB, "DATA_DIR": t.TempDir()}
			for k, v := range tt.env {
				m[k] = v
			}
			c, err := config.Load(env(m))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if c.LogsUI != tt.want {
				t.Errorf("LogsUI = %v, want %v", c.LogsUI, tt.want)
			}
		})
	}
}

func TestLoad_TMDBMockFixtureDoesNotEnableHardcover(t *testing.T) {
	c, err := config.Load(env(map[string]string{
		"DATABASE_URL": validDB, "DATA_DIR": t.TempDir(), "TMDB_MOCK_FIXTURE": "true",
	}))
	if err != nil || c.HardcoverAPIKey != "" {
		t.Fatalf("Load() = %q, %v", c.HardcoverAPIKey, err)
	}
}

func TestLoad_Identity(t *testing.T) {
	const team, aud = "team.cloudflareaccess.com", "aud-tag"
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string // empty = must succeed
	}{
		{"development with nothing refuses", map[string]string{}, "no identity provider"},
		{"development with dev identity", map[string]string{"DEV_IDENTITY": "true"}, ""},
		{"development with access", map[string]string{"ACCESS_TEAM_DOMAIN": team, "ACCESS_AUD": aud}, ""},
		{"development with owner email only (LAN owner)", map[string]string{"OWNER_EMAIL": "me@example.test"}, ""},
		{"leftover LAN_OWNER_FALLBACK is ignored", map[string]string{"LAN_OWNER_FALLBACK": "maybe", "DEV_IDENTITY": "true"}, ""},
		{"production with access and owner", map[string]string{"APP_ENV": "production", "ACCESS_TEAM_DOMAIN": team, "ACCESS_AUD": aud, "OWNER_EMAIL": "me@example.test"}, ""},
		{"production without owner email", map[string]string{"APP_ENV": "production", "ACCESS_TEAM_DOMAIN": team, "ACCESS_AUD": aud}, "requires OWNER_EMAIL"},
		{"production without access", map[string]string{"APP_ENV": "production", "OWNER_EMAIL": "me@example.test"}, "requires ACCESS_TEAM_DOMAIN and ACCESS_AUD"},
		{"production with dev identity only", map[string]string{"APP_ENV": "production", "DEV_IDENTITY": "true"}, "requires ACCESS_TEAM_DOMAIN"},
		{"production refuses dev identity", map[string]string{"APP_ENV": "production", "ACCESS_TEAM_DOMAIN": team, "ACCESS_AUD": aud, "DEV_IDENTITY": "true"}, "DEV_IDENTITY cannot be enabled"},
		{"production owner email alone", map[string]string{"APP_ENV": "production", "OWNER_EMAIL": "me@example.test"}, "requires ACCESS_TEAM_DOMAIN"},
		{"team without aud", map[string]string{"ACCESS_TEAM_DOMAIN": team, "DEV_IDENTITY": "true"}, "must be set together"},
		{"aud without team", map[string]string{"ACCESS_AUD": aud, "DEV_IDENTITY": "true"}, "must be set together"},
		{"team with path", map[string]string{"ACCESS_TEAM_DOMAIN": "team.cloudflareaccess.com/x", "ACCESS_AUD": aud}, "host name"},
		{"team without dot", map[string]string{"ACCESS_TEAM_DOMAIN": "team", "ACCESS_AUD": aud}, "host name"},
		{"bad owner email", map[string]string{"OWNER_EMAIL": "nope", "DEV_IDENTITY": "true"}, "OWNER_EMAIL must be an e-mail"},
		{"owner email with two at signs", map[string]string{"OWNER_EMAIL": "a@b@c", "DEV_IDENTITY": "true"}, "OWNER_EMAIL must be an e-mail"},
		{"bad dev flag", map[string]string{"DEV_IDENTITY": "maybe"}, "DEV_IDENTITY must be true or false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{"DATABASE_URL": validDB, "DATA_DIR": t.TempDir()}
			for k, v := range tt.env {
				m[k] = v
			}
			_, err := config.Load(rawEnv(m))
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

// D-150: a leftover LAN_OWNER_FALLBACK (any value) never blocks startup and yields one warning.
func TestLoad_LegacyLANOwnerFallbackWarns(t *testing.T) {
	for _, v := range []string{"true", "false", "maybe"} {
		c, err := config.Load(rawEnv(map[string]string{
			"DATABASE_URL": validDB, "DATA_DIR": t.TempDir(), "DEV_IDENTITY": "true", "LAN_OWNER_FALLBACK": v,
		}))
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "LAN_OWNER_FALLBACK is ignored") {
			t.Fatalf("%s: warnings = %v", v, c.Warnings)
		}
	}
}

func TestLoad_IdentityValues(t *testing.T) {
	c, err := config.Load(rawEnv(map[string]string{
		"DATABASE_URL": validDB, "DATA_DIR": t.TempDir(),
		"ACCESS_TEAM_DOMAIN": " https://team.cloudflareaccess.com/ ", "ACCESS_AUD": " aud ",
		"OWNER_EMAIL": " Me@Example.TEST ", "DEV_IDENTITY": "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessTeamDomain != "team.cloudflareaccess.com" || c.AccessAUD != "aud" || c.OwnerEmail != "me@example.test" ||
		!c.DevIdentity || len(c.Warnings) != 0 {
		t.Fatalf("identity = %+v", c)
	}
	if got := c.DevIdentities(); len(got) != 3 || got[0] != "me@example.test" || got[1] != "alice@dev.test" || got[2] != "bob@dev.test" {
		t.Errorf("DevIdentities = %v", got)
	}
	if got := (config.Config{}).DevIdentities(); got[0] != "owner@dev.test" {
		t.Errorf("default owner = %v", got)
	}
}
