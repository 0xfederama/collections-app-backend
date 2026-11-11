package users

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Reader is what GET /me reads through.
type Reader interface {
	Get(ctx context.Context, id uuid.UUID) (User, error)
}

// Register adds the account operations. devIdentities is non-nil only when DEV_IDENTITY is on.
func Register(api huma.API, log *slog.Logger, svc Reader, devIdentities []string) {
	h := &handler{svc: svc, dev: devIdentities}
	huma.Register(api, huma.Operation{
		OperationID: "getMe", Method: http.MethodGet, Path: "/api/v1/me", Tags: []string{"users"},
		Summary:     "Get the signed-in account",
		Description: "The account of the caller, created on the first request. `dev_identities` is only set on dev/test servers.",
		Errors:      []int{http.StatusUnauthorized, http.StatusInternalServerError},
	}, httpserver.Op(log, h.getMe))
}

type handler struct {
	svc Reader
	dev []string
}

func (h *handler) getMe(ctx context.Context, _ *struct{}) (*MeOutput, error) {
	id, ok := scope.UserID(ctx)
	if !ok {
		return nil, problem.Unauthenticated()
	}
	u, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &MeOutput{Body: MeJSON{ID: u.ID, Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL, DevIdentities: h.dev}}, nil
}
