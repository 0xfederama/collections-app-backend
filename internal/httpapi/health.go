package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
)

// readinessTimeout bounds the database ping and the DATA_DIR probe of /readyz.
const readinessTimeout = 2 * time.Second

// registerHealth adds the two probes. They are plain text, not JSON, and answer
// without a workspace or a service: liveness never touches the database or storage.
func registerHealth(api huma.API, d Deps) {
	tags := []string{"health"}
	huma.Register(api, huma.Operation{
		OperationID: "checkHealth", Method: http.MethodGet, Path: "/healthz", Tags: tags,
		Summary:     "Liveness probe",
		Description: "200 if the process is up.",
	}, httpserver.Op(d.Logger, checkHealth))
	huma.Register(api, huma.Operation{
		OperationID: "checkReadiness", Method: http.MethodGet, Path: "/readyz", Tags: tags,
		Summary:     "Readiness probe",
		Description: "200 if the database is reachable and `DATA_DIR` is writable, else 503.",
	}, httpserver.Op(d.Logger, checkReadiness(d)))
}

func checkHealth(context.Context, *struct{}) (*huma.StreamResponse, error) {
	return plainText(http.StatusOK, "ok\n"), nil
}

// checkReadiness reports 200 only when the database answers and DATA_DIR is
// writable. Failures return a terse body: no paths, SQL or driver errors.
func checkReadiness(d Deps) func(context.Context, *struct{}) (*huma.StreamResponse, error) {
	return func(ctx context.Context, _ *struct{}) (*huma.StreamResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
		defer cancel()

		if err := d.DB.Ping(ctx); err != nil {
			d.Logger.Warn("readiness: database ping failed", "err", err)
			return plainText(http.StatusServiceUnavailable, "database unavailable\n"), nil
		}
		if err := checkWritable(d.DataDir); err != nil {
			d.Logger.Warn("readiness: data dir not writable", "err", err)
			return plainText(http.StatusServiceUnavailable, "storage unavailable\n"), nil
		}
		return plainText(http.StatusOK, "ready\n"), nil
	}
}

// plainText streams a text/plain body with the given status, the way the probes
// have always answered.
func plainText(status int, body string) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(ctx huma.Context) {
		ctx.SetHeader("Content-Type", "text/plain")
		ctx.SetStatus(status)
		_, _ = ctx.BodyWriter().Write([]byte(body))
	}}
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
