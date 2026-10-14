package httpapi

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

// Server implements oapi.StrictServerInterface. Operations not implemented yet
// live in unimplemented.go; each endpoint task moves its methods into its own
// file (settings.go, folders.go, …) so parallel PRs do not collide.
type Server struct {
	deps Deps
}

var _ oapi.StrictServerInterface = (*Server)(nil)

func newServer(d Deps) *Server { return &Server{deps: d} }

func (s *Server) CheckHealth(context.Context, oapi.CheckHealthRequestObject) (oapi.CheckHealthResponseObject, error) {
	return oapi.CheckHealth200TextResponse("ok\n"), nil
}

// CheckReadiness reports 200 only when the database answers and DATA_DIR is
// writable. Failures return a terse body: no paths, SQL or driver errors.
func (s *Server) CheckReadiness(ctx context.Context, _ oapi.CheckReadinessRequestObject) (oapi.CheckReadinessResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if err := s.deps.DB.Ping(ctx); err != nil {
		s.deps.Logger.Warn("readiness: database ping failed", "err", err)
		return oapi.CheckReadiness503TextResponse("database unavailable\n"), nil
	}
	if err := checkWritable(s.deps.DataDir); err != nil {
		s.deps.Logger.Warn("readiness: data dir not writable", "err", err)
		return oapi.CheckReadiness503TextResponse("storage unavailable\n"), nil
	}
	return oapi.CheckReadiness200TextResponse("ready\n"), nil
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
