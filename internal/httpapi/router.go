// Package httpapi holds the HTTP router, middleware, problem+json mapping and
// the handlers behind the generated strict server.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/clock"
	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/trash"
	"github.com/0xfederama/collections-app-backend/internal/tree"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
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
	Clock   clock.Clock

	// WorkspaceID is the single workspace, from workspace.Bootstrap.
	WorkspaceID uuid.UUID
	Settings    *workspace.Service
	// LookupAvailable reports whether the optional server-side TMDB key is configured (F013-B5).
	LookupAvailable bool
	// TMDB serves optional movie and TV metadata lookups (F013-B1..B3).
	TMDB *tmdb.Client
	// Tree serves folders, lists and the sidebar tree (B-06).
	Tree *tree.Service
	// Items serves items (B-07).
	Items *items.Service
	// Content serves Markdown content and its versions (B-08).
	Content *content.Service
	// Search serves GET /search (B-11).
	Search *search.Service
	// Images serves item images and attachment files (B-09).
	Images *images.Service
	// Trash serves deletes, the trash list, restore and purge (B-10).
	Trash *trash.Service
}

// NewRouter builds the HTTP handler: the generated strict server mounted on
// chi, behind request id, security headers, access log and panic recovery.
func NewRouter(d Deps) (http.Handler, error) {
	if d.Clock == nil {
		d.Clock = clock.System{}
	}
	return newHandler(newServer(d), d)
}

// newHandler mounts any strict server implementation; tests use it to inject
// handlers that fail or panic.
func newHandler(si oapi.StrictServerInterface, d Deps) (http.Handler, error) {
	doc, err := oapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("load embedded openapi spec: %w", err)
	}

	errs := handlerErrors{log: d.Logger}
	strict := oapi.NewStrictHandlerWithOptions(si, nil, oapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  errs.request,
		ResponseErrorHandlerFunc: errs.response,
	})

	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, newProblem(http.StatusNotFound, "not_found", "Not found", "No such endpoint."))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeProblem(w, newProblem(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed",
			"This endpoint does not support that method."))
	})
	oapi.HandlerWithOptions(strict, oapi.ChiServerOptions{
		BaseRouter:       r,
		Middlewares:      []oapi.MiddlewareFunc{newGuard(doc).middleware},
		ErrorHandlerFunc: errs.parameter,
	})

	return wrap(r, d.Logger), nil
}

// wrap adds the cross-cutting middleware, outermost first: request id,
// security headers, access log, panic recovery.
func wrap(h http.Handler, log *slog.Logger) http.Handler {
	h = recoverer(log)(h)
	h = accessLog(log)(h)
	h = securityHeaders(h)
	return requestID(h)
}
