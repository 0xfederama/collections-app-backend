// Package httpapi holds the HTTP router, handlers and middleware.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
)

// Pinger is satisfied by *pgxpool.Pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Deps are the collaborators the router needs.
type Deps struct {
	DB      Pinger
	DataDir string
	Logger  *slog.Logger
}

// NewRouter builds the HTTP handler. Further middleware lands in B-04.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", readyHandler(d))
	return r
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeText(w, http.StatusOK, "ok")
}

// readyHandler reports 200 only when the database answers and DATA_DIR is
// writable. Failures return a terse body: no paths, SQL or driver errors.
func readyHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := d.DB.Ping(ctx); err != nil {
			d.Logger.Warn("readiness: database ping failed", "err", err)
			writeText(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		if err := checkWritable(d.DataDir); err != nil {
			d.Logger.Warn("readiness: data dir not writable", "err", err)
			writeText(w, http.StatusServiceUnavailable, "storage unavailable")
			return
		}
		writeText(w, http.StatusOK, "ready")
	}
}

func checkWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".readyz-*")
	if err != nil {
		return fmt.Errorf("create probe file: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("close probe file: %w", err)
	}
	return os.Remove(name)
}

func writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
