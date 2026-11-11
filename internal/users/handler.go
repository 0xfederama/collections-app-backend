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
	List(ctx context.Context) ([]User, error)
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
	huma.Register(api, huma.Operation{
		OperationID: "listUsers", Method: http.MethodGet, Path: "/api/v1/users", Tags: []string{"users"},
		Summary:     "List the known users",
		Description: "Every user who has signed in, ordered by e-mail: the names behind `*_user_id` fields and the sharing picker. Any signed-in user may read it.",
		Errors:      []int{http.StatusUnauthorized, http.StatusInternalServerError},
	}, httpserver.Op(log, h.listUsers))
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
	return &MeOutput{Body: MeJSON{ID: u.ID, Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL, IdentitySource: scope.IdentitySource(ctx), DevIdentities: h.dev}}, nil
}

func (h *handler) listUsers(ctx context.Context, _ *struct{}) (*UserListOutput, error) {
	if _, ok := scope.UserID(ctx); !ok {
		return nil, problem.Unauthenticated()
	}
	us, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	data := make([]UserJSON, 0, len(us))
	for _, u := range us {
		data = append(data, UserJSON{ID: u.ID, Email: u.Email, Name: u.Name, AvatarURL: u.AvatarURL})
	}
	return &UserListOutput{Body: UserListJSON{Data: data}}, nil
}
