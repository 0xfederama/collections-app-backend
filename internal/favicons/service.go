package favicons

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// fetchTimeout bounds one whole lookup (page, icon candidates, redirects).
const fetchTimeout = 10 * time.Second

// Fetcher finds the icon of an origin ("https://host[:port]"). It returns ErrNoIcon when
// there is none and ErrBlocked when the origin points at a non-public address.
type Fetcher interface {
	Fetch(ctx context.Context, origin string) (Icon, error)
}

// Service serves favicons through a disk cache.
type Service struct {
	fetch Fetcher
	files storage.FileStore
	clk   clock.Clock
	log   *slog.Logger

	mu       sync.Mutex
	inflight map[string]*call // one fetch per cache key at a time
}

type call struct {
	done chan struct{}
	icon Icon
	err  error
}

// NewService builds the service. files holds the cache under the "favicons/" prefix.
func NewService(fetch Fetcher, files storage.FileStore, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{fetch: fetch, files: files, clk: clk, log: log, inflight: map[string]*call{}}
}

// meta is the sidecar of a cached answer. Miss is the negative cache.
type meta struct {
	Miss        bool      `json:"miss"`
	ContentType string    `json:"content_type,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

// fresh reports whether the answer is still inside its TTL.
func (m meta) fresh(now time.Time) bool {
	ttl := HitTTL
	if m.Miss {
		ttl = MissTTL
	}
	age := now.Sub(m.FetchedAt)
	return age >= 0 && age < ttl
}

// Get returns the favicon of the site that rawURL points to. Errors: validation (bad or
// refused URL) and apperr.FaviconNotFound.
func (s *Service) Get(ctx context.Context, rawURL string) (Icon, error) {
	origin, key, err := Normalize(rawURL)
	if err != nil {
		return Icon{}, err
	}
	if m, ok := s.readMeta(key); ok && m.fresh(s.clk.Now()) {
		if m.Miss {
			return Icon{}, apperr.FaviconNotFound()
		}
		if icon, ok := s.readIcon(key, m); ok {
			return icon, nil
		}
	}
	return s.single(ctx, key, origin)
}

// Normalize validates rawURL and returns the site origin and the cache key
// (sha256 of the lowercased host and port).
func Normalize(rawURL string) (origin, key string, err error) {
	msg := "must be an absolute http or https URL of at most 2048 characters"
	if strings.TrimSpace(rawURL) == "" || len(rawURL) > MaxURLLength {
		return "", "", apperr.Validation(apperr.FieldError{Field: "url", Message: msg})
	}
	u, perr := url.Parse(rawURL)
	if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", "", apperr.Validation(apperr.FieldError{Field: "url", Message: msg})
	}
	hostport := strings.ToLower(u.Hostname())
	if strings.Contains(hostport, ":") {
		hostport = "[" + hostport + "]"
	}
	if p := u.Port(); p != "" {
		hostport += ":" + p
	}
	sum := sha256.Sum256([]byte(hostport))
	return u.Scheme + "://" + hostport, hex.EncodeToString(sum[:]), nil
}

func iconKey(key string) string { return "favicons/" + key + ".img" }
func metaKey(key string) string { return "favicons/" + key + ".json" }

func (s *Service) readIcon(key string, m meta) (Icon, bool) {
	rc, _, err := s.files.Open(iconKey(key))
	if err != nil {
		return Icon{}, false
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, MaxIconBytes+1))
	if err != nil || len(data) > MaxIconBytes {
		return Icon{}, false
	}
	return Icon{ContentType: m.ContentType, Data: data}, true
}

func (s *Service) readMeta(key string) (meta, bool) {
	rc, _, err := s.files.Open(metaKey(key))
	if err != nil {
		return meta{}, false
	}
	defer func() { _ = rc.Close() }()
	var m meta
	if err := json.NewDecoder(io.LimitReader(rc, 4096)).Decode(&m); err != nil {
		return meta{}, false
	}
	return m, true
}

// single runs the lookup once per key: concurrent callers wait for the first one.
func (s *Service) single(ctx context.Context, key, origin string) (Icon, error) {
	s.mu.Lock()
	c, running := s.inflight[key]
	if !running {
		c = &call{done: make(chan struct{})}
		s.inflight[key] = c
	}
	s.mu.Unlock()
	if running {
		select {
		case <-c.done:
			return c.icon, c.err
		case <-ctx.Done():
			return Icon{}, ctx.Err()
		}
	}
	// The fetch must outlive the first caller's request: other callers wait on it.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	defer cancel()
	c.icon, c.err = s.run(fctx, key, origin)
	s.mu.Lock()
	delete(s.inflight, key)
	s.mu.Unlock()
	close(c.done)
	return c.icon, c.err
}

func (s *Service) run(ctx context.Context, key, origin string) (Icon, error) {
	icon, err := s.fetch.Fetch(ctx, origin)
	switch {
	case err == nil:
		s.store(key, meta{ContentType: icon.ContentType, FetchedAt: s.clk.Now()}, icon.Data)
		return icon, nil
	case errors.Is(err, ErrBlocked):
		// Not cached: nothing was fetched, and the DNS answer may change.
		return Icon{}, apperr.Validation(apperr.FieldError{Field: "url", Message: "points to an address that is not allowed"})
	case errors.Is(err, ErrNoIcon):
		s.store(key, meta{Miss: true, FetchedAt: s.clk.Now()}, nil)
		return Icon{}, apperr.FaviconNotFound()
	default:
		// Network trouble (timeout, refused) is not cached as a day-long miss.
		s.log.Debug("favicon fetch failed", "err", err)
		return Icon{}, apperr.FaviconNotFound()
	}
}

// store writes the bytes before the sidecar: the sidecar is what makes an entry visible.
// Cache write failures are logged, never returned: the fetched icon is still served.
func (s *Service) store(key string, m meta, data []byte) {
	if !m.Miss {
		if _, err := s.files.Put(iconKey(key), bytes.NewReader(data)); err != nil {
			s.log.Warn("favicon cache write failed", "err", err)
			return
		}
	}
	b, _ := json.Marshal(m)
	if _, err := s.files.Put(metaKey(key), bytes.NewReader(b)); err != nil {
		s.log.Warn("favicon cache write failed", "err", err)
	}
}
