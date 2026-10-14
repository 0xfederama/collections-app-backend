package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

func TestToProblem(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		check      func(t *testing.T, p problem)
	}{
		{"not found", apperr.NotFound("item"), 404, "not_found", func(t *testing.T, p problem) {
			if p.Detail != "Item not found." {
				t.Errorf("detail = %q", p.Detail)
			}
		}},
		{"version conflict", apperr.VersionConflict(8), 412, "version_conflict", func(t *testing.T, p problem) {
			if p.CurrentVersion == nil || *p.CurrentVersion != 8 {
				t.Errorf("current_version = %v, want 8", p.CurrentVersion)
			}
		}},
		{"version conflict at 0 still reports the version", apperr.VersionConflict(0), 412, "version_conflict", func(t *testing.T, p problem) {
			if p.CurrentVersion == nil || *p.CurrentVersion != 0 {
				t.Errorf("current_version = %v, want 0", p.CurrentVersion)
			}
		}},
		{"validation", apperr.Validation(apperr.FieldError{Field: "price.max", Message: "must be >= min"}, apperr.FieldError{Field: "title", Message: "required"}), 422, "validation_failed", func(t *testing.T, p problem) {
			if len(p.Errors) != 2 || p.Errors[0].Field != "price.max" || p.Errors[1].Message != "required" {
				t.Errorf("errors = %+v", p.Errors)
			}
		}},
		{"cycle", apperr.Cycle(), 409, "cycle_detected", nil},
		{"parent in trash", apperr.ParentInTrash(apperr.BlockingParent{Type: "folder", ID: "f1", Name: "Movies"}), 409, "parent_in_trash", func(t *testing.T, p problem) {
			if p.BlockingParent == nil || *p.BlockingParent != (blockingParent{Type: "folder", ID: "f1", Name: "Movies"}) {
				t.Errorf("blocking_parent = %+v", p.BlockingParent)
			}
		}},
		{"destination unavailable", apperr.DestinationUnavailable(), 409, "destination_unavailable", nil},
		{"order stale", apperr.OrderStale(), 409, "order_stale", nil},
		{"payload too large", apperr.PayloadTooLarge(1), 413, "payload_too_large", nil},
		{"MaxBytesError from the reader", &http.MaxBytesError{Limit: 1}, 413, "payload_too_large", nil},
		{"unsupported media type", apperr.UnsupportedMediaType("image/svg+xml"), 415, "unsupported_media_type", nil},
		{"not implemented", apperr.NotImplemented(), 501, "not_implemented", nil},
		{"wrapped domain error", fmt.Errorf("move: %w", apperr.Cycle()), 409, "cycle_detected", nil},
		{"joined domain error", errors.Join(errors.New("ctx"), apperr.NotFound("list")), 404, "not_found", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, unexpected := toProblem(tt.err, "rid")
			if unexpected {
				t.Fatal("reported as unexpected")
			}
			if p.Status != tt.wantStatus || p.Code != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", p.Status, p.Code, tt.wantStatus, tt.wantCode)
			}
			if p.RequestID != "" {
				t.Errorf("request_id %q set on a non-500 problem", p.RequestID)
			}
			if want := problemTypeBase + strings.ReplaceAll(tt.wantCode, "_", "-"); p.Type != want {
				t.Errorf("type = %q, want %q", p.Type, want)
			}
			if tt.check != nil {
				tt.check(t, p)
			}

			rec := httptest.NewRecorder()
			writeProblem(rec, p)
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var back map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"type", "title", "status", "code"} {
				if _, ok := back[k]; !ok {
					t.Errorf("body lacks %q: %s", k, rec.Body)
				}
			}
		})
	}
}

func TestToProblem_UnknownErrorIsOpaque(t *testing.T) {
	secret := errors.New(`pq: relation "items" does not exist at /srv/app/internal/items/repo.go:42 SELECT * FROM items`)
	p, unexpected := toProblem(secret, "req-1")
	if !unexpected || p.Status != 500 || p.Code != "internal" || p.RequestID != "req-1" {
		t.Fatalf("got %+v unexpected=%v", p, unexpected)
	}
	rec := httptest.NewRecorder()
	writeProblem(rec, p)
	assertNoInternals(t, rec.Body.String())
}

func assertNoInternals(t *testing.T, body string) {
	t.Helper()
	for _, bad := range []string{"pq:", "relation", "SELECT", "/srv/", ".go", "goroutine", "panic", "kaboom", "secret", "runtime"} {
		if strings.Contains(body, bad) {
			t.Errorf("body leaks %q: %s", bad, body)
		}
	}
}

// failingServer returns canned results for GetTree so tests can drive the error paths.
type failingServer struct {
	*Server
	err   error
	panic any
}

func (f failingServer) ListItems(context.Context, oapi.ListItemsRequestObject) (oapi.ListItemsResponseObject, error) {
	return nil, f.err
}

func (f failingServer) GetTree(context.Context, oapi.GetTreeRequestObject) (oapi.GetTreeResponseObject, error) {
	if f.panic != nil {
		panic(f.panic)
	}
	return nil, f.err
}

func injected(t *testing.T, s failingServer) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	d := Deps{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	s.Server = newServer(d)
	h, err := newHandler(s, d)
	if err != nil {
		t.Fatal(err)
	}
	return wrap(h, d.Logger), &logs
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

func TestHandlerErrorsMapThroughResponseErrorHandler(t *testing.T) {
	h, _ := injected(t, failingServer{err: apperr.VersionConflict(3)})
	rec := get(t, h, "/api/v1/tree")
	if rec.Code != 412 || !strings.Contains(rec.Body.String(), `"current_version":3`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestInternalErrorIsOpaqueAndLogged(t *testing.T) {
	h, logs := injected(t, failingServer{err: errors.New(`pq: relation "items" does not exist /srv/app/x.go:1 SELECT secret`)})
	rec := get(t, h, "/api/v1/tree")
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	assertNoInternals(t, rec.Body.String())
	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	rid := rec.Header().Get("X-Request-Id")
	if p.Code != "internal" || p.RequestID == "" || p.RequestID != rid {
		t.Errorf("problem = %+v, header id %q", p, rid)
	}
	if !strings.Contains(logs.String(), "SELECT secret") || !strings.Contains(logs.String(), rid) {
		t.Errorf("server log should keep the detail and the request id: %s", logs)
	}
	assertHeaders(t, rec)
}

func TestPanicBecomes500Problem(t *testing.T) {
	h, logs := injected(t, failingServer{panic: "kaboom: secret internals"})
	rec := get(t, h, "/api/v1/tree")
	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	assertNoInternals(t, rec.Body.String())
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q", ct)
	}
	assertHeaders(t, rec)
	if !strings.Contains(logs.String(), "kaboom") || !strings.Contains(logs.String(), `"status":500`) {
		t.Errorf("panic and the 500 access-log line should be logged: %s", logs)
	}
	// The server survives: the next request works.
	if rec := get(t, h, "/healthz"); rec.Code != 200 {
		t.Errorf("after panic: healthz = %d", rec.Code)
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
	h, logs := injected(t, failingServer{err: apperr.NotImplemented()})
	get(t, h, "/api/v1/lists/0192f4a0-1111-7000-8000-000000000001/items?q=private+words")
	line := logs.String()
	for _, want := range []string{`"method":"GET"`, `"route":"/api/v1/lists/{id}/items"`, `"status":501`, `"duration_ms"`, `"request_id"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log lacks %s: %s", want, line)
		}
	}
	if strings.Contains(line, "private") {
		t.Errorf("log leaks query text: %s", line)
	}
}

func TestPanicAfterHeadersWrittenDoesNotDoubleWrite(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := requestID(recoverer(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic("late")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want the already-written 202", rec.Code)
	}
}

// The multipart body is not buffered by the guard, so the limit has to hold
// while the (future) handler streams it.
func TestMultipartBodyIsCappedWhileStreaming(t *testing.T) {
	op := &operation{id: "SetItemImage", bodies: map[string]*openapi3.Schema{"multipart/form-data": nil}}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", io.NopCloser(io.LimitReader(zeros{}, multipartBodyLimit+10)))
	r.ContentLength = -1
	r.Header.Set("Content-Type", "multipart/form-data; boundary=b")

	r, p := op.checkBody(httptest.NewRecorder(), r)
	if p.Code != "" {
		t.Fatalf("unexpected problem %+v", p)
	}
	_, err := io.Copy(io.Discard, r.Body)
	var tooBig *http.MaxBytesError
	if !errors.As(err, &tooBig) || tooBig.Limit != multipartBodyLimit {
		t.Fatalf("err = %v, want MaxBytesError with limit %d", err, multipartBodyLimit)
	}
	if p, _ := toProblem(err, ""); p.Status != 413 {
		t.Errorf("a handler returning that error maps to %d, want 413", p.Status)
	}
}

type zeros struct{}

func (zeros) Read(b []byte) (int, error) { clear(b); return len(b), nil }
