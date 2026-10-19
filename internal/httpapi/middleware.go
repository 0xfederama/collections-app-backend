package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxIfMatchVersion
)

const requestIDHeader = "X-Request-Id"

// safeRequestID accepts a client-supplied id only if it is a short, log-safe token.
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// IfMatchVersion returns the version N of the validated `If-Match: "<prefix>-N"`
// header. It is only meaningful in handlers of operations that require If-Match
// (the request guard rejects the request before the handler otherwise).
func IfMatchVersion(ctx context.Context) int {
	v, _ := ctx.Value(ctxIfMatchVersion).(int)
	return v
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

// requestID puts an id on every response, including 404/405/413/500.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// securityHeaders sets the fixed hardening headers (no CORS, D-033).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// accessLog writes one structured line per request: no bodies, no query string.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
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
				"request_id", requestIDFrom(r.Context()),
			)
		})
	}
}

// recoverer turns a panic into a 500 problem. The stack goes to the log only.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
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
				id := requestIDFrom(r.Context())
				log.ErrorContext(r.Context(), "panic in handler", "panic", p, "request_id", id, "stack", string(debug.Stack()))
				if rec.wrote {
					return // too late to change the response
				}
				prob, _ := toProblem(errPanic, id)
				writeProblem(w, prob)
			}()
			next.ServeHTTP(rec, r)
		})
	}
}
