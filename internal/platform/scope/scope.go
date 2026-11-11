// Package scope carries the workspace and the user a request acts on. Repositories read it
// from the context, so adding accounts later changes this package, not every query.
package scope

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type key struct{}

type userKey struct{}

// WithUser returns ctx carrying the authenticated user.
func WithUser(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey{}, userID)
}

// UserID returns the user of ctx; ok is false when the request has no identity.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userKey{}).(uuid.UUID)
	return id, ok && id != uuid.Nil
}

type sourceKey struct{}

// Identity sources: how the identity middleware resolved the caller (GET /me identity_source, D-151).
const (
	SourceAccess = "access" // a verified Cloudflare Access token
	SourceLAN    = "lan"    // the home network: no Cloudflare edge header, acts as OWNER_EMAIL (D-150)
	SourceDev    = "dev"    // a dev identity (DEV_IDENTITY=true)
)

// WithIdentitySource returns ctx carrying how the caller was identified.
func WithIdentitySource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// IdentitySource returns how the caller was identified. A context without one (tests that inject
// a user directly) reports SourceAccess, the source that shows the full account UI.
func IdentitySource(ctx context.Context) string {
	if s, ok := ctx.Value(sourceKey{}).(string); ok && s != "" {
		return s
	}
	return SourceAccess
}

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
