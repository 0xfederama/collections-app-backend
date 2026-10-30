package logs_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/logs"
)

type logsResponse struct {
	Data []struct {
		Time  time.Time      `json:"time"`
		Level string         `json:"level"`
		Msg   string         `json:"msg"`
		Attrs map[string]any `json:"attrs"`
	} `json:"data"`
	Total int `json:"total"`
}

// newLogsRouter serves the log operations over buf. The second return is a logger
// that writes into buf, at debug level.
func newLogsRouter(buf *logs.Buffer) (http.Handler, *slog.Logger) {
	r := chi.NewRouter()
	api := httpserver.NewAPI(r)
	logs.Register(api, slog.New(slog.NewTextHandler(io.Discard, nil)), buf)
	out := slog.New(logs.NewHandler(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), buf))
	return r, out
}

func serve(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	return rec
}

// TestListLogsFilters: level, since and limit, the newest records last, and the
// total of the matches before the limit.
func TestListLogsFilters(t *testing.T) {
	buf := logs.NewBuffer(10)
	h, log := newLogsRouter(buf)
	before := time.Now().Add(-time.Hour)
	log.Debug("d")
	log.Info("i", "password", "hunter2")
	log.Warn("w")
	log.Error("e")

	tests := []struct {
		name      string
		target    string
		wantMsgs  []string
		wantTotal int
	}{
		{"default keeps everything", "/admin/logs", []string{"d", "i", "w", "e"}, 4},
		{"level warn", "/admin/logs?level=warn", []string{"w", "e"}, 2},
		{"level error", "/admin/logs?level=error", []string{"e"}, 1},
		{"limit keeps newest", "/admin/logs?limit=2", []string{"w", "e"}, 4},
		{"since in the past", "/admin/logs?since=" + before.UTC().Format(time.RFC3339), []string{"d", "i", "w", "e"}, 4},
		{"since in the future", "/admin/logs?since=" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339), []string{}, 0},
		{"level and limit", "/admin/logs?level=info&limit=1", []string{"e"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(t, h, tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q", ct)
			}
			var got logsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			msgs := []string{}
			for _, e := range got.Data {
				msgs = append(msgs, e.Msg)
			}
			if strings.Join(msgs, ",") != strings.Join(tt.wantMsgs, ",") {
				t.Errorf("data = %v, want %v", msgs, tt.wantMsgs)
			}
			if got.Total != tt.wantTotal {
				t.Errorf("total = %d, want %d", got.Total, tt.wantTotal)
			}
		})
	}
}

// TestListLogsShape: the entry fields, lower-case levels, redacted secrets, and an
// empty data array (not null) when nothing matches.
func TestListLogsShape(t *testing.T) {
	buf := logs.NewBuffer(10)
	h, log := newLogsRouter(buf)
	log.Info("login", "user", "bob", "token", "abc")

	rec := serve(t, h, "/admin/logs")
	var got logsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Data) != 1 {
		t.Fatalf("data = %+v", got.Data)
	}
	e := got.Data[0]
	if e.Level != "info" || e.Msg != "login" || e.Time.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if e.Attrs["user"] != "bob" || e.Attrs["token"] != "[redacted]" {
		t.Errorf("attrs = %v", e.Attrs)
	}
	if strings.Contains(rec.Body.String(), "abc") {
		t.Error("secret value leaked into the response")
	}

	empty := serve(t, h, "/admin/logs?level=error")
	if !strings.Contains(empty.Body.String(), `"data":[]`) {
		t.Errorf("empty answer = %s, want data []", empty.Body.String())
	}
}

// TestListLogsRejectsBadParameters: values outside the documented set answer a 4xx problem.
func TestListLogsRejectsBadParameters(t *testing.T) {
	h, _ := newLogsRouter(logs.NewBuffer(10))
	for _, target := range []string{
		"/admin/logs?level=verbose",
		"/admin/logs?since=yesterday",
		"/admin/logs?limit=0",
		"/admin/logs?limit=2001",
	} {
		t.Run(target, func(t *testing.T) {
			rec := serve(t, h, target)
			if rec.Code < 400 || rec.Code >= 500 {
				t.Errorf("status = %d, want 4xx", rec.Code)
			}
		})
	}
}

// TestViewLogsPage: the page is HTML, self-contained, and sends a CSP whose nonce
// matches the page's own inline style and script. Text goes in with textContent only.
func TestViewLogsPage(t *testing.T) {
	h, _ := newLogsRouter(logs.NewBuffer(10))
	rec := serve(t, h, "/admin/logs/view")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	page := rec.Body.String()
	if strings.Contains(page, "__NONCE__") {
		t.Error("nonce placeholder not replaced")
	}

	csp := rec.Header().Get("Content-Security-Policy")
	m := regexp.MustCompile(`'nonce-([A-Za-z0-9+/=]+)'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("CSP %q has no nonce", csp)
	}
	if !strings.HasPrefix(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
	if n := strings.Count(page, `nonce="`+m[1]+`"`); n != 2 {
		t.Errorf("page has %d elements with the CSP nonce, want 2 (style and script)", n)
	}

	for _, banned := range []string{"innerHTML", "http://", "https://", "<link ", "<img ", "@import"} {
		if strings.Contains(page, banned) {
			t.Errorf("page contains %q; it must be self-contained and use textContent", banned)
		}
	}
	if !strings.Contains(page, "textContent") || !strings.Contains(page, "'../logs?'") {
		t.Error("page does not render through textContent or does not fetch ../logs")
	}
}

// TestViewLogsNonceIsFresh: every response gets its own nonce.
func TestViewLogsNonceIsFresh(t *testing.T) {
	h, _ := newLogsRouter(logs.NewBuffer(10))
	a := serve(t, h, "/admin/logs/view").Header().Get("Content-Security-Policy")
	b := serve(t, h, "/admin/logs/view").Header().Get("Content-Security-Policy")
	if a == b {
		t.Errorf("two responses share the CSP %q", a)
	}
}

// TestLogOperationsAreHiddenFromSpec: the operations are routed but not in the public OpenAPI document.
func TestLogOperationsAreHiddenFromSpec(t *testing.T) {
	r := chi.NewRouter()
	api := httpserver.NewAPI(r)
	logs.Register(api, slog.New(slog.NewTextHandler(io.Discard, nil)), logs.NewBuffer(10))
	doc := api.OpenAPI()
	for path := range doc.Paths {
		if strings.HasPrefix(path, "/admin") {
			t.Errorf("%s is in the spec, want hidden", path)
		}
	}
	if len(doc.Paths) != 0 {
		t.Errorf("log operations added %d paths to the spec", len(doc.Paths))
	}
}
