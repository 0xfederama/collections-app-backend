package httpapi

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/access"
	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// guardRule maps the operations under a path prefix to the kind of resource their id parameter
// names. It is the explicit list the path guard works from: an operation with a path parameter
// that no rule covers fails TestGuardCoversEveryPathParam, so a new endpoint cannot skip it.
type guardRule struct {
	prefix string
	param  string
	kind   access.Kind
	// missing is the resource name of the 404 problem, the one the operation answered before.
	missing string
}

var guardRules = []guardRule{
	{"/api/v1/folders/{id}", "id", access.KindFolder, "folder"},
	{"/api/v1/lists/{id}", "id", access.KindList, "list"},
	{"/api/v1/items/{id}", "id", access.KindItem, "item"},
	{"/api/v1/attachments/{id}", "id", access.KindAttachment, "attachment"},
	{"/api/v1/trash/{batch_id}", "batch_id", access.KindTrashBatch, "trash entry"},
}

// plainParams are path parameters that name no resource of their own: a number inside an already
// guarded path, or the user of a member of the guarded folder (a user is not a box).
var plainParams = map[string]bool{"version": true, "user_id": true}

// ruleFor returns the rule of an operation path, if one covers it.
func ruleFor(path string) (guardRule, bool) {
	for _, r := range guardRules {
		if path == r.prefix || strings.HasPrefix(path, r.prefix+"/") {
			return r, true
		}
	}
	return guardRule{}, false
}

// guard is the Huma middleware that answers 404 for a resource the caller cannot see, before the
// If-Match check and before the body is read: a missing resource and another user's are the same
// answer, so nothing tells them apart. An id that does not parse is left to Huma (400 as before).
// Operations without a path parameter pass through; the rule test keeps that list honest.
func guard(api huma.API, svc *access.Service, log *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		rule, ok := ruleFor(ctx.Operation().Path)
		if !ok {
			next(ctx)
			return
		}
		id, err := uuid.Parse(ctx.Param(rule.param))
		if err != nil {
			next(ctx)
			return
		}
		visible, err := svc.Visible(ctx.Context(), rule.kind, id)
		if err != nil {
			rid := httpserver.RequestIDFrom(ctx.Context())
			p, _ := problem.From(err, rid)
			log.ErrorContext(ctx.Context(), "access check failed", "err", err, "request_id", rid)
			_ = huma.WriteErr(api, ctx, p.Status, "", p)
			return
		}
		if !visible {
			p, _ := problem.From(apperr.NotFound(rule.missing), httpserver.RequestIDFrom(ctx.Context()))
			_ = huma.WriteErr(api, ctx, http.StatusNotFound, "", p)
			return
		}
		next(ctx)
	}
}

// accessCache gives every request its own access snapshot cache (read at most once).
func accessCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(access.WithCache(r.Context())))
	})
}

// newAccess builds the access rules over the database, or nil when Deps.DB is not a pool (the
// probe-only routers of some tests: they serve no data).
func newAccess(db Pinger) *access.Service {
	pg, ok := db.(access.DB)
	if !ok {
		return nil
	}
	return access.NewService(access.NewPostgres(pg))
}
