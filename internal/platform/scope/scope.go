// Package scope carries the workspace a request acts on. Repositories read it
// from the context, so adding accounts later changes this package, not every query.
package scope

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type key struct{}

// With returns ctx scoped to the workspace.
func With(ctx context.Context, workspaceID uuid.UUID) context.Context {
	return context.WithValue(ctx, key{}, workspaceID)
}

// WorkspaceID returns the workspace of ctx. It is uuid.Nil on an unscoped
// context, which matches no row.
func WorkspaceID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(key{}).(uuid.UUID)
	return id
}

// Middleware scopes every request to workspaceID (the single workspace today).
func Middleware(workspaceID uuid.UUID) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(With(r.Context(), workspaceID)))
		})
	}
}
