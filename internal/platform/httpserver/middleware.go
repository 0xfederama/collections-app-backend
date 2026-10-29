package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

const requestIDHeader = "X-Request-Id"

// statusClientClosedRequest is the (nginx) status for a request the client abandoned.
const statusClientClosedRequest = 499

// errPanic is mapped like any unexpected error: a generic 500.
var errPanic = errors.New("panic")

// safeRequestID accepts a client-supplied id only if it is a short, log-safe token.
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestIDFrom returns the request id of ctx, or "" when there is none.
func RequestIDFrom(ctx context.Context) string {
	return problem.RequestIDFrom(ctx)
}

// statusRecorder remembers what was written so the access log and the panic
// recovery know the status and whether the header is already out.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status, s.wrote = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status, s.wrote = http.StatusOK, true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (deadlines, flush).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Wrap adds the cross-cutting middleware, outermost first: request id,
// security headers, access log, panic recovery.
func Wrap(h http.Handler, log *slog.Logger) http.Handler {
	h = Recoverer(log)(h)
	h = AccessLog(log)(h)
	h = SecurityHeaders(h)
	return RequestID(h)
}

// RequestID puts an id on every response, including 404/405/413/500. A safe
// client-supplied X-Request-Id is kept, anything else gets a new UUID.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(problem.WithRequestID(r.Context(), id)))
	})
}

// SecurityHeaders sets the fixed hardening headers (no CORS, D-033).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// AccessLog writes one structured line per request: no bodies, no query string.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			// Own the chi route context so the matched pattern is readable
			// here after the router (which would otherwise create a private one).
			rctx := chi.NewRouteContext()
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
			next.ServeHTTP(rec, r)

			route := "unmatched"
			if p := rctx.RoutePattern(); p != "" {
				route = p
			}
			level := slog.LevelInfo
			if route == "/healthz" || route == "/readyz" {
				level = slog.LevelDebug // probes would drown the log
			}
			if rec.status == statusClientClosedRequest {
				level = slog.LevelDebug // the client went away, not a server problem
			}
			log.Log(r.Context(), level, "request",
				"method", r.Method,
				"route", route,
				"status", rec.status,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
			)
		})
	}
}

// Recoverer turns a panic into a 500 problem. The stack goes to the log only.
func Recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler { //nolint:errorlint // net/http compares this sentinel by identity
					panic(p)
				}
				id := RequestIDFrom(r.Context())
				log.ErrorContext(r.Context(), "panic in handler", "panic", p, "request_id", id, "stack", string(debug.Stack()))
				if rec.wrote {
					return // too late to change the response
				}
				prob, _ := problem.From(errPanic, id)
				writeProblem(w, prob)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// NotFound answers an unknown path with the 404 problem (chi's NotFound handler).
func NotFound(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, problem.New(http.StatusNotFound, "not_found", "Not found", "No such endpoint."))
}

// MethodNotAllowed answers a known path with an unsupported method (chi's
// MethodNotAllowed handler).
func MethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeProblem(w, problem.New(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed",
		"This endpoint does not support that method."))
}

// writeProblem sends p as application/problem+json.
func writeProblem(w http.ResponseWriter, p *problem.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
