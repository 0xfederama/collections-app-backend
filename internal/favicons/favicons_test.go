package favicons_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/favicons"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// A 1x1 PNG header is enough for content sniffing.
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 32)...)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newService(t *testing.T, o favicons.Options) (*favicons.Service, *testClock) {
	t.Helper()
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clk := &testClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return favicons.NewService(favicons.NewHTTPFetcher(o), store, clk, slog.New(slog.DiscardHandler)), clk
}

// loop is the option every httptest-based test needs: the servers listen on 127.0.0.1.
var loop = favicons.Options{AllowLoopback: true}

func wantNotFound(t *testing.T, err error) {
	t.Helper()
	var nf *apperr.FaviconNotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want favicon not found", err)
	}
}

func wantURLField(t *testing.T, err error) {
	t.Helper()
	var v *apperr.ValidationError
	if !errors.As(err, &v) || v.Errors[0].Field != "url" {
		t.Fatalf("err = %v, want validation error on url", err)
	}
}

func TestFavicon_LinkRelIconRelativeHref(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html><head><LINK rel="stylesheet" href="/a.css"><link href='assets/i.png?a=1&amp;b=2' rel='shortcut ICON'></head>`))
		case "/assets/i.png":
			if r.URL.RawQuery != "a=1&b=2" {
				t.Errorf("query = %q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "text/plain") // declared type is ignored
			_, _ = w.Write(pngBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	icon, err := svc.Get(t.Context(), srv.URL+"/some/page?x=1")
	if err != nil {
		t.Fatal(err)
	}
	if icon.ContentType != "image/png" || !bytes.Equal(icon.Data, pngBytes) {
		t.Fatalf("icon = %q, %d bytes", icon.ContentType, len(icon.Data))
	}
}

func TestFavicon_FallbackToFaviconIco(t *testing.T) {
	ico := append([]byte{0, 0, 1, 0, 1, 0}, bytes.Repeat([]byte{0}, 40)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<html>no icon here</html>`))
		case "/favicon.ico":
			_, _ = w.Write(ico)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	icon, err := svc.Get(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if icon.ContentType != "image/x-icon" {
		t.Fatalf("content type = %q", icon.ContentType)
	}
}

func TestFavicon_HTMLDeclaredIconThatIsNotAnImageFallsBack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<link rel="icon" href="/x.svg">`))
		case "/x.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
		case "/favicon.ico":
			_, _ = w.Write(pngBytes)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	icon, err := svc.Get(t.Context(), srv.URL)
	if err != nil || icon.ContentType != "image/png" {
		t.Fatalf("icon = %+v, err = %v (SVG must be refused)", icon, err)
	}
}

func TestFavicon_NegativeCacheAndTTL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	svc, clk := newService(t, loop)
	_, err := svc.Get(t.Context(), srv.URL)
	wantNotFound(t, err)
	first := hits.Load()
	if first == 0 {
		t.Fatal("server never called")
	}
	_, err = svc.Get(t.Context(), srv.URL+"/other/path")
	wantNotFound(t, err)
	if hits.Load() != first {
		t.Fatalf("negative cache not used: %d calls, want %d", hits.Load(), first)
	}
	clk.Advance(favicons.MissTTL + time.Second)
	_, err = svc.Get(t.Context(), srv.URL)
	wantNotFound(t, err)
	if hits.Load() == first {
		t.Fatal("expired miss was not refetched")
	}
}

func TestFavicon_CacheHitWithoutRefetch(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(pngBytes)
		}
	}))
	defer srv.Close()
	svc, clk := newService(t, loop)
	if _, err := svc.Get(t.Context(), srv.URL); err != nil {
		t.Fatal(err)
	}
	first := hits.Load()
	clk.Advance(HitTTLMinusOne())
	icon, err := svc.Get(t.Context(), srv.URL+"/a")
	if err != nil || icon.ContentType != "image/png" {
		t.Fatalf("icon = %+v, err = %v", icon, err)
	}
	if hits.Load() != first {
		t.Fatalf("cache hit refetched: %d calls, want %d", hits.Load(), first)
	}
	clk.Advance(2 * time.Second)
	if _, err := svc.Get(t.Context(), srv.URL); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == first {
		t.Fatal("expired hit was not refetched")
	}
}

func HitTTLMinusOne() time.Duration { return favicons.HitTTL - time.Second }

func TestFavicon_ConcurrentRequestsFetchOnce(t *testing.T) {
	var icoHits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			icoHits.Add(1)
			<-release
			_, _ = w.Write(pngBytes)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := svc.Get(context.Background(), srv.URL); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if icoHits.Load() != 1 {
		t.Fatalf("favicon.ico fetched %d times, want 1", icoHits.Load())
	}
}

func TestFavicon_SizeLimit(t *testing.T) {
	big := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, favicons.MaxIconBytes)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(big)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	_, err := svc.Get(t.Context(), srv.URL)
	wantNotFound(t, err)
}

func TestFavicon_Timeout(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-done:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(done)
	svc, _ := newService(t, favicons.Options{AllowLoopback: true, RequestTimeout: 150 * time.Millisecond})
	start := time.Now()
	_, err := svc.Get(t.Context(), srv.URL)
	wantNotFound(t, err)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestFavicon_RefusesNonPublicAddresses(t *testing.T) {
	svc, _ := newService(t, favicons.Options{}) // loopback NOT allowed
	for _, u := range []string{
		"http://127.0.0.1/", "http://127.0.0.1:8080/x", "http://localhost/", "http://[::1]/",
		"http://10.0.0.5/", "http://172.16.0.1/", "http://192.168.1.1/", "http://[fd00::1]/",
		"http://169.254.169.254/latest/meta-data", "http://[fe80::1]/", "http://100.64.0.1/",
		"http://0.0.0.0/", "http://[::]/", "http://224.0.0.1/", "http://255.255.255.255/",
		"http://[::ffff:10.0.0.1]/", "http://[::ffff:127.0.0.1]/", "http://[::ffff:169.254.169.254]/",
	} {
		t.Run(u, func(t *testing.T) {
			_, err := svc.Get(t.Context(), u)
			wantURLField(t, err)
		})
	}
	// A refused address is not written to the negative cache.
	_, err := svc.Get(t.Context(), "http://127.0.0.1/")
	wantURLField(t, err)
}

func TestFavicon_AllowLoopbackKeepsOtherRangesRefused(t *testing.T) {
	svc, _ := newService(t, loop)
	_, err := svc.Get(t.Context(), "http://10.0.0.1/")
	wantURLField(t, err)
}

func TestFavicon_RedirectToPrivateAddressRefused(t *testing.T) {
	var secret atomic.Int32
	// The public-looking origin (allowed loopback) redirects to a link-local address.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret.Add(1)
		http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	_, err := svc.Get(t.Context(), srv.URL)
	wantURLField(t, err)
}

func TestFavicon_IconHrefToPrivateAddressIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<link rel="icon" href="http://10.1.2.3/i.png">`))
		case "/favicon.ico":
			_, _ = w.Write(pngBytes)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	icon, err := svc.Get(t.Context(), srv.URL)
	if err != nil || icon.ContentType != "image/png" {
		t.Fatalf("icon = %+v, err = %v", icon, err)
	}
}

func TestFavicon_TooManyRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	_, err := svc.Get(t.Context(), srv.URL)
	wantNotFound(t, err)
}

func TestFavicon_RedirectWithinLimitFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/favicon.ico":
			http.Redirect(w, r, "/real.png", http.StatusMovedPermanently)
		case "/real.png":
			_, _ = w.Write(pngBytes)
		}
	}))
	defer srv.Close()
	svc, _ := newService(t, loop)
	if _, err := svc.Get(t.Context(), srv.URL); err != nil {
		t.Fatal(err)
	}
}

func TestFavicon_NormalizeRejectsBadURLs(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", favicons.MaxURLLength)
	for _, u := range []string{"", "   ", "ftp://example.com", "file:///etc/passwd", "javascript:alert(1)",
		"//example.com", "example.com", "http://", "https://:80/", long, "http://%zz/"} {
		_, _, err := favicons.Normalize(u)
		if err == nil {
			t.Errorf("Normalize(%q) accepted", u)
			continue
		}
		wantURLField(t, err)
	}
}

func TestFavicon_NormalizeOriginAndKey(t *testing.T) {
	o1, k1, err := favicons.Normalize("HTTPS://Example.COM/a/b?c=d#e")
	if err != nil {
		t.Fatal(err)
	}
	o2, k2, _ := favicons.Normalize("https://example.com:443/z")
	_, k3, _ := favicons.Normalize("https://example.com:8443/")
	if o1 != "https://example.com" || o2 != "https://example.com:443" {
		t.Fatalf("origins = %q, %q", o1, o2)
	}
	if k1 == k3 || k1 == k2 || len(k1) != 64 {
		t.Fatalf("keys: %q %q %q", k1, k2, k3)
	}
	_, k4, _ := favicons.Normalize("http://example.com/")
	if k1 != k4 {
		t.Fatal("same host must share the key across schemes")
	}
}

type fakeFetcher struct{ err error }

func (f fakeFetcher) Fetch(context.Context, string) (favicons.Icon, error) {
	return favicons.Icon{}, f.err
}

func TestFavicon_HandlerResponses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/favicon.ico" {
			_, _ = w.Write(pngBytes)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	store, err := storage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	svc := favicons.NewService(favicons.NewHTTPFetcher(loop), store, &testClock{t: time.Now()}, slog.New(slog.DiscardHandler))
	r := chi.NewRouter()
	favicons.Register(httpserver.NewAPI(r), slog.New(slog.DiscardHandler), svc)

	get := func(u string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/favicon?url="+url.QueryEscape(u), nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	rec := get(srv.URL)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("Cache-Control") != "public, max-age=604800" || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatalf("200 case: %d %v", rec.Code, rec.Header())
	}

	// Private address: 422 with a message on the url field.
	svc2 := favicons.NewService(favicons.NewHTTPFetcher(favicons.Options{}), store, &testClock{t: time.Now()}, slog.New(slog.DiscardHandler))
	r2 := chi.NewRouter()
	favicons.Register(httpserver.NewAPI(r2), slog.New(slog.DiscardHandler), svc2)
	rec = httptest.NewRecorder()
	r2.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/favicon?url=http%3A%2F%2F192.168.0.1%2F", nil))
	var p struct {
		Code   string `json:"code"`
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != 422 || len(p.Errors) != 1 || p.Errors[0].Field != "url" {
		t.Fatalf("422 case: %d %s", rec.Code, rec.Body.String())
	}

	// No icon: 404 favicon_not_found.
	empty := httptest.NewServer(http.NotFoundHandler())
	defer empty.Close()
	rec = get(empty.URL)
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != 404 || p.Code != "favicon_not_found" {
		t.Fatalf("404 case: %d %s", rec.Code, rec.Body.String())
	}

	// Fetcher errors that are not "no icon" also end as 404, never 500.
	svc3 := favicons.NewService(fakeFetcher{err: errors.New("boom")}, store, &testClock{t: time.Now()}, slog.New(slog.DiscardHandler))
	if _, err := svc3.Get(t.Context(), "https://example.org"); err == nil {
		t.Fatal("want error")
	}
}
