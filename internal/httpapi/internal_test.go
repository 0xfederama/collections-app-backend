package httpapi_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// probe builds the middleware stack of the real router (httpserver.NewAPI and
// httpserver.Wrap) around one test-only operation, GET /probe. fail runs in the
// handler: it returns the error to answer with, or panics. Unknown errors, panics
// and client aborts are answered and logged here, as they are for every route.
func probe(log *slog.Logger, fail func(ctx context.Context) error) http.Handler {
	r := chi.NewRouter()
	api := httpserver.NewAPI(r)
	huma.Register(api, huma.Operation{OperationID: "probe", Method: http.MethodGet, Path: "/probe"},
		httpserver.Op(log, func(ctx context.Context, _ *struct{}) (*struct{}, error) {
			return nil, fail(ctx)
		}))
	return httpserver.Wrap(r, log)
}

// probeGet sends a GET and returns the recorder. The probe ignores its query.
func probeGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

// debugLogger logs everything into buf, so the tests can read the access log and the errors.
func debugLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// assertNoInternals checks a body leaks neither driver/SQL text nor stack traces.
func assertNoInternals(t *testing.T, body string) {
	t.Helper()
	for _, bad := range []string{"pq:", "relation", "SELECT", "/srv/", ".go", "goroutine", "panic", "kaboom", "secret", "runtime"} {
		if strings.Contains(body, bad) {
			t.Errorf("body leaks %q: %s", bad, body)
		}
	}
}

func TestHandlerErrorsBecomeProblems(t *testing.T) {
	h := probe(quietLogger(), func(context.Context) error { return apperr.VersionConflict(3) })
	rec := probeGet(t, h, "/probe?q=x")
	p := wantProblem(t, rec, 412, "version_conflict")
	if p.CurrentVersion == nil || *p.CurrentVersion != 3 {
		t.Fatalf("current_version = %v, want 3", p.CurrentVersion)
	}
}

func TestInternalErrorIsOpaqueAndLogged(t *testing.T) {
	var logs bytes.Buffer
	h := probe(debugLogger(&logs), func(context.Context) error {
		return errors.New(`pq: relation "items" does not exist /srv/app/x.go:1 SELECT secret`)
	})
	rec := probeGet(t, h, "/probe?q=x")
	assertNoInternals(t, rec.Body.String())
	p := wantProblem(t, rec, 500, "internal")
	rid := rec.Header().Get("X-Request-Id")
	if p.RequestID == "" || p.RequestID != rid {
		t.Errorf("problem request_id = %q, header id %q", p.RequestID, rid)
	}
	if !strings.Contains(logs.String(), "SELECT secret") || !strings.Contains(logs.String(), rid) {
		t.Errorf("server log should keep the detail and the request id: %s", logs.String())
	}
	assertHeaders(t, rec)
}

func TestPanicBecomes500Problem(t *testing.T) {
	var logs bytes.Buffer
	h := probe(debugLogger(&logs), func(context.Context) error { panic("kaboom: secret internals") })
	rec := probeGet(t, h, "/probe?q=x")
	assertNoInternals(t, rec.Body.String())
	wantProblem(t, rec, 500, "internal")
	assertHeaders(t, rec)
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), `"status":500`) {
		t.Errorf("panic and the 500 access-log line should be logged: %s", logs.String())
	}
	// The server survives: the next request works.
	if rec := probeGet(t, h, "/no-such-path"); rec.Code != http.StatusNotFound {
		t.Errorf("after panic: unknown path = %d", rec.Code)
	}
}

func assertHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, k := range []string{"X-Request-Id", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Content-Security-Policy"} {
		if rec.Header().Get(k) == "" {
			t.Errorf("missing %s", k)
		}
	}
}

func TestAccessLogHasNoQueryOrBody(t *testing.T) {
	var logs bytes.Buffer
	h := probe(debugLogger(&logs), func(context.Context) error { return apperr.NotFound("list") })
	probeGet(t, h, "/probe?q=private+words")
	line := logs.String()
	for _, want := range []string{`"method":"GET"`, `"route":"/probe"`, `"status":404`, `"duration_ms"`, `"request_id"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log lacks %s: %s", want, line)
		}
	}
	if strings.Contains(line, "private") {
		t.Errorf("log leaks query text: %s", line)
	}
}

func TestF016_ClientAbortIsNot500(t *testing.T) {
	canceledCtx := func(t *testing.T) context.Context {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		return ctx
	}
	for _, tc := range []struct {
		name       string
		ctx        func(t *testing.T) context.Context
		err        error
		wantStatus int
		wantError  bool // "unhandled error" at ERROR
	}{
		{"canceled request", canceledCtx, context.Canceled, 499, false},
		{"canceled and wrapped", canceledCtx, fmt.Errorf("query: %w", context.Canceled), 499, false},
		{"canceled error but live request", func(t *testing.T) context.Context { return t.Context() }, context.Canceled, 500, true},
		{"deadline exceeded stays a 500", canceledCtx, context.DeadlineExceeded, 500, true},
		{"other error on a canceled request", canceledCtx, errors.New("boom"), 500, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := probe(debugLogger(&logs), func(context.Context) error { return tc.err })
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(tc.ctx(t), http.MethodGet, "/probe?q=x", nil))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			out := logs.String()
			if got := strings.Contains(out, "unhandled error"); got != tc.wantError {
				t.Errorf("unhandled error logged = %v, want %v: %s", got, tc.wantError, out)
			}
			wantAccess := fmt.Sprintf(`"msg":"request","method":"GET","route":"/probe","status":%d`, tc.wantStatus)
			if !strings.Contains(out, wantAccess) {
				t.Errorf("access log lacks status %d: %s", tc.wantStatus, out)
			}
			if tc.wantStatus == 499 && strings.Contains(out, `"level":"ERROR"`) || tc.wantStatus == 499 && strings.Contains(out, `"level":"INFO"`) {
				t.Errorf("a client abort must log at DEBUG only: %s", out)
			}
		})
	}
}
