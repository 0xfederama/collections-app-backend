// Package config parses and validates environment configuration. It fails fast
// with clear messages so a misconfigured container never starts half-working.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config holds every runtime setting of the API process.
type Config struct {
	AppEnv          string
	HTTPAddr        string
	DatabaseURL     string
	DBMaxConns      int32
	DataDir         string
	WorkspaceName   string
	TMDBAPIKey      string
	TMDBMockFixture bool
	// OpenLibraryContact (an e-mail) goes into the Open Library User-Agent. Optional: identified clients get 3 requests/second instead of 1.
	OpenLibraryContact string
	// HardcoverAPIKey is the optional Hardcover token (optional gap-filler of the automatic book lookup). Blank disables it; never required.
	HardcoverAPIKey string
	LogLevel        slog.Level
	LogFormat       string
}

// Load reads configuration through getenv (os.Getenv in production) and returns
// every validation problem at once.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		AppEnv:          get("APP_ENV", "development"),
		HTTPAddr:        get("HTTP_ADDR", ":8080"),
		DatabaseURL:     get("DATABASE_URL", ""),
		DataDir:         get("DATA_DIR", "/data"),
		WorkspaceName:   get("WORKSPACE_NAME", "Our Collection"),
		TMDBAPIKey:      strings.TrimSpace(getenv("TMDB_API_KEY")),
		TMDBMockFixture: false,
		LogFormat:       get("LOG_FORMAT", "json"),
	}
	if raw := strings.TrimSpace(getenv("TMDB_MOCK_FIXTURE")); raw != "" {
		fixture, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("TMDB_MOCK_FIXTURE must be true or false"))
		} else {
			c.TMDBMockFixture = fixture
		}
	}

	c.OpenLibraryContact = strings.TrimSpace(getenv("OPENLIBRARY_CONTACT"))
	c.HardcoverAPIKey = strings.TrimSpace(getenv("HARDCOVER_API_KEY"))

	if c.AppEnv != "development" && c.AppEnv != "production" {
		errs = append(errs, fmt.Errorf("APP_ENV must be development or production, got %q", c.AppEnv))
	}
	if c.AppEnv == "production" && c.TMDBMockFixture {
		errs = append(errs, errors.New("TMDB_MOCK_FIXTURE cannot be enabled when APP_ENV=production"))
	}

	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		errs = append(errs, fmt.Errorf("HTTP_ADDR %q is not a valid listen address (host:port)", c.HTTPAddr))
	}

	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	} else if u, err := url.Parse(c.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		// Never echo the value: it contains the password.
		errs = append(errs, errors.New("DATABASE_URL must be a postgres:// URL with a host"))
	}

	maxConns, err := strconv.ParseInt(get("DB_MAX_CONNS", "10"), 10, 32)
	if err != nil || maxConns < 1 || maxConns > 100 {
		errs = append(errs, errors.New("DB_MAX_CONNS must be an integer between 1 and 100"))
	}
	c.DBMaxConns = int32(maxConns)

	if err := c.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", getenv("LOG_LEVEL")))
	}

	if c.LogFormat != "json" && c.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", c.LogFormat))
	}

	if fi, err := os.Stat(c.DataDir); err != nil || !fi.IsDir() {
		errs = append(errs, fmt.Errorf("DATA_DIR %q must be an existing directory", c.DataDir))
	} else if f, err := os.CreateTemp(c.DataDir, ".startup-*"); err != nil {
		errs = append(errs, fmt.Errorf("DATA_DIR %q must be writable", c.DataDir))
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %w", errors.Join(errs...))
	}
	return c, nil
}
