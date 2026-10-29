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

	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/search"
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
	Definitions *definitions.Service
	// LookupAvailable reports whether the optional server-side TMDB key is configured (F013-B5).
	LookupAvailable bool
	// BookLookupAvailable reports whether book lookup (Open Library) is on (F016-B3).
	BookLookupAvailable bool
	// TMDB serves optional movie and TV title suggestions (F013-B1..B3).
	TMDB lookup.Provider
	// Books serves book title suggestions (Open Library, F016-B3).
	Books lookup.Provider
	// Lookup orchestrates item imports and refreshes (F013-B3).
	Lookup *lookup.Service
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
	r.NotFound(httpserver.NotFound)
	r.MethodNotAllowed(httpserver.MethodNotAllowed)
	oapi.HandlerWithOptions(strict, oapi.ChiServerOptions{
		BaseRouter:       r,
		Middlewares:      []oapi.MiddlewareFunc{newGuard(doc).middleware},
		ErrorHandlerFunc: errs.parameter,
	})

	return httpserver.Wrap(r, d.Logger), nil
}
