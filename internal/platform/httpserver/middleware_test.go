package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

func TestRequestID(t *testing.T) {
	long := strings.Repeat("a", 65)
	tests := []struct {
		name   string
		in     string
		wantID string // "" means: any new id, not the client's
	}{
		{"safe client id is kept", "req-42.a_B", "req-42.a_B"},
		{"id with a space is replaced", "bad id", ""},
		{"id over 64 bytes is replaced", long, ""},
		{"blank id is replaced", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var inCtx, inProblem string
			h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				inCtx = RequestIDFrom(r.Context())
				inProblem = problem.RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			if tt.in != "" {
				req.Header.Set("X-Request-Id", tt.in)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-Id")
			if got == "" || (tt.wantID != "" && got != tt.wantID) || (tt.wantID == "" && got == tt.in) {
				t.Errorf("response X-Request-Id = %q for client id %q", got, tt.in)
			}
			if inCtx != got || inProblem != got {
				t.Errorf("context ids = %q / %q, want the header id %q (one key)", inCtx, inProblem, got)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestNotFoundAndMethodNotAllowedAreProblems(t *testing.T) {
	r := chi.NewRouter()
	r.NotFound(NotFound)
	r.MethodNotAllowed(MethodNotAllowed)
	r.Get("/items", func(http.ResponseWriter, *http.Request) {})
	h := RequestID(r)

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{"unknown path", http.MethodGet, "/nope", 404, "not_found", "No such endpoint."},
		{"wrong method", http.MethodPost, "/items", 405, "method_not_allowed", "This endpoint does not support that method."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var p map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("not JSON: %s (%v)", rec.Body, err)
			}
			if p["code"] != tt.wantCode || p["detail"] != tt.wantDetail {
				t.Errorf("problem = %v, want code %s detail %q", p, tt.wantCode, tt.wantDetail)
			}
			if _, ok := p["request_id"]; ok {
				t.Errorf("request_id set on a non-internal problem: %v", p)
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("missing X-Request-Id")
			}
		})
	}
}

func TestRecovererPanicBecomes500Problem(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom: secret internals")
	}), log)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "kaboom") || strings.Contains(body, "secret") {
		t.Errorf("body leaks the panic: %s", body)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p["code"] != "internal" || p["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Errorf("problem = %v", p)
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Errorf("panic should be logged: %s", logs.String())
	}
}

func TestPanicAfterHeadersWrittenDoesNotDoubleWrite(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := RequestID(Recoverer(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want the already-written 202", rec.Code)
	}
}

func TestAccessLogUsesTheMatchedPatternAndNoQuery(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := chi.NewRouter()
	r.Get("/api/v1/lists/{id}/items", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	r.Get("/healthz", func(http.ResponseWriter, *http.Request) {})
	h := Wrap(r, log)

	for _, path := range []string{"/api/v1/lists/0192f4a0-1111-7000-8000-000000000001/items?q=private+words", "/nope"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	}
	out := logs.String()
	for _, want := range []string{`"route":"/api/v1/lists/{id}/items"`, `"status":404`, `"route":"unmatched"`, `"duration_ms"`, `"request_id"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s: %s", want, out)
		}
	}
	if strings.Contains(out, "private") || strings.Contains(out, "0192f4a0") {
		t.Errorf("log leaks the query or the concrete path: %s", out)
	}
}
