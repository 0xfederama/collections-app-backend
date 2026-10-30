package httpserver

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// IfMatchFirst is the middleware of an operation that requires If-Match: it answers
// 428 precondition_required for a missing or malformed value, or one with another
// resource's prefix, before the body is read. A broken request must not hide the
// missing precondition. prefix is the ETag prefix of the resource (etag.Item, ...).
// The handler still parses the version itself.
func IfMatchFirst(api huma.API, prefix string) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if _, ok := etag.Parse(ctx.Header("If-Match"), prefix); !ok {
			_ = huma.WriteErr(api, ctx, http.StatusPreconditionRequired, "", problem.PreconditionRequired())
			return
		}
		next(ctx)
	}
}
