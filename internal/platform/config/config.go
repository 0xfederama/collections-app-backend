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
	TMDBAPIKey      string
	TMDBMockFixture bool
	// OpenLibraryContact (an e-mail) goes into the Open Library User-Agent. Optional: identified clients get 3 requests/second instead of 1.
	OpenLibraryContact string
	// HardcoverAPIKey is the optional Hardcover token (optional gap-filler of the automatic book lookup). Blank disables it; never required.
	HardcoverAPIKey string
	LogLevel        slog.Level
	LogFormat       string
	// LogsUI serves the in-memory log buffer and its viewer (/admin/logs). Off by default in production.
	LogsUI bool

	// AccessTeamDomain is the Cloudflare Access team host (no scheme, no trailing slash), AccessAUD the
	// Access application's audience tag. Both set or both empty (F-027).
	AccessTeamDomain string
	AccessAUD        string
	// OwnerEmail is the lower-cased e-mail of the instance owner.
	OwnerEmail string
	// LANOwnerFallback serves requests without a Cloudflare edge header as OwnerEmail (emergency switch).
	LANOwnerFallback bool
	// DevIdentity enables the fake dev users. Never allowed with APP_ENV=production.
	DevIdentity bool
}

// DevOwnerEmail is the first dev identity when OWNER_EMAIL is unset.
const DevOwnerEmail = "owner@dev.test"

// DevIdentities are the e-mails a dev request may act as: the owner first, then two fixed users.
func (c Config) DevIdentities() []string {
	owner := c.OwnerEmail
	if owner == "" {
		owner = DevOwnerEmail
	}
	return []string{owner, "alice@dev.test", "bob@dev.test"}
}

// NormalizeTeamDomain accepts ACCESS_TEAM_DOMAIN with or without https:// and a trailing slash.
func NormalizeTeamDomain(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "https://")
	return strings.TrimRight(s, "/")
}

func parseBoolEnv(getenv func(string) string, key string, errs *[]error) bool {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s must be true or false", key))
	}
	return v
}

// validateIdentity checks the Access / dev identity / LAN fallback settings (F-027).
func validateIdentity(c Config) []error {
	var errs []error
	if (c.AccessTeamDomain == "") != (c.AccessAUD == "") {
		errs = append(errs, errors.New("ACCESS_TEAM_DOMAIN and ACCESS_AUD must be set together"))
	}
	if c.AccessTeamDomain != "" && (strings.ContainsAny(c.AccessTeamDomain, "/?#@ ") || !strings.Contains(c.AccessTeamDomain, ".")) {
		errs = append(errs, errors.New("ACCESS_TEAM_DOMAIN must be a host name such as myteam.cloudflareaccess.com"))
	}
	if c.LANOwnerFallback && c.OwnerEmail == "" {
		errs = append(errs, errors.New("LAN_OWNER_FALLBACK=true requires OWNER_EMAIL"))
	}
	hasAccess := c.AccessTeamDomain != "" && c.AccessAUD != ""
	switch c.AppEnv {
	case "production":
		if !hasAccess {
			errs = append(errs, errors.New("APP_ENV=production requires ACCESS_TEAM_DOMAIN and ACCESS_AUD"))
		}
		if c.DevIdentity {
			errs = append(errs, errors.New("DEV_IDENTITY cannot be enabled when APP_ENV=production"))
		}
	case "development":
		if !hasAccess && !c.DevIdentity && !c.LANOwnerFallback {
			errs = append(errs, errors.New("no identity provider: set ACCESS_TEAM_DOMAIN and ACCESS_AUD, DEV_IDENTITY=true or LAN_OWNER_FALLBACK=true"))
		}
	}
	return errs
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

	c.LogsUI = c.AppEnv != "production"
	if raw := strings.TrimSpace(getenv("LOGS_UI")); raw != "" {
		on, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, errors.New("LOGS_UI must be true or false"))
		} else {
			c.LogsUI = on
		}
	}

	c.AccessTeamDomain = NormalizeTeamDomain(getenv("ACCESS_TEAM_DOMAIN"))
	c.AccessAUD = strings.TrimSpace(getenv("ACCESS_AUD"))
	c.OwnerEmail = strings.ToLower(strings.TrimSpace(getenv("OWNER_EMAIL")))
	if c.OwnerEmail != "" && !validEmail(c.OwnerEmail) {
		errs = append(errs, errors.New("OWNER_EMAIL must be an e-mail address"))
	}
	c.LANOwnerFallback = parseBoolEnv(getenv, "LAN_OWNER_FALLBACK", &errs)
	c.DevIdentity = parseBoolEnv(getenv, "DEV_IDENTITY", &errs)
	errs = append(errs, validateIdentity(c)...)

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

// validEmail is a basic check (one @, non-empty parts, no spaces, 3 to 320 characters), not RFC 5322.
func validEmail(e string) bool {
	if len(e) < 3 || len(e) > 320 || strings.ContainsAny(e, " \t\r\n") {
		return false
	}
	at := strings.Index(e, "@")
	return at > 0 && at == strings.LastIndex(e, "@") && at < len(e)-1
}
