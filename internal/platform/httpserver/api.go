// Package httpserver builds the Huma API on a chi router.
package httpserver

import (
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// DefaultMaxBodyBytes is the body limit of every operation that does not
// declare its own MaxBodyBytes (uploads declare a larger one).
const DefaultMaxBodyBytes = 64 << 10

// humaDefaultBodyBytes is what Huma sets for an operation that declares no
// MaxBodyBytes. Huma does not record whether the value was declared, so an
// operation that declares exactly this limit gets DefaultMaxBodyBytes instead:
// declare a different limit (e.g. 2 MiB) when 1 MiB is what you want.
const humaDefaultBodyBytes = 1 << 20

// NewAPI returns the Huma API mounted on r. The spec is generated from the
// registered operations (api.OpenAPI()); no docs or spec route is served.
// Handlers should be registered through Op, so that their errors are logged and
// answered the same way everywhere.
func NewAPI(r chi.Router) huma.API {
	problem.Install()
	cfg := huma.DefaultConfig("Collection App API", "1.0.0")
	cfg.DocsPath, cfg.OpenAPIPath, cfg.SchemasPath = "", "", ""
	cfg.CreateHooks = nil // no "$schema" property in responses
	cfg.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", func(t reflect.Type, hint string) string {
		return strings.TrimSuffix(huma.DefaultSchemaNamer(t, hint), "JSON")
	})
	cfg.OnAddOperation = append(cfg.OnAddOperation, defaultBodyLimit, documentUnauthenticated)
	api := humachi.New(r, cfg)
	api.UseMiddleware(requireContentType(api))
	return api
}

// defaultBodyLimit gives every operation that kept Huma's 1 MiB default the
// DefaultMaxBodyBytes limit. It runs when the operation is added, after Huma
// has set that default.
func defaultBodyLimit(_ *huma.OpenAPI, op *huma.Operation) {
	if op.MaxBodyBytes == humaDefaultBodyBytes {
		op.MaxBodyBytes = DefaultMaxBodyBytes
	}
}

// documentUnauthenticated adds the 401 answer to every /api/v1 operation: the identity
// middleware (F-027) can refuse any of them. The body is the Problem every other error uses.
func documentUnauthenticated(_ *huma.OpenAPI, op *huma.Operation) {
	if !strings.HasPrefix(op.Path, "/api/v1/") {
		return
	}
	if _, ok := op.Responses["401"]; ok {
		return
	}
	src := op.Responses["500"]
	if src == nil {
		src = op.Responses["default"]
	}
	if src == nil {
		return
	}
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	op.Responses["401"] = &huma.Response{Description: http.StatusText(http.StatusUnauthorized), Content: src.Content}
}

// requireContentType answers 400 for a request that has a body but no
// Content-Type. Huma would otherwise read such a body as JSON. A wrong
// Content-Type is rejected by the error mapping (415 becomes 400).
func requireContentType(api huma.API) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if ctx.Operation().RequestBody != nil && ctx.Header("Content-Type") == "" && hasBody(ctx) {
			_ = huma.WriteErr(api, ctx, http.StatusBadRequest, "", problem.UnsupportedContentType())
			return
		}
		next(ctx)
	}
}

// hasBody reports whether the request carries a body. The length comes from
// the underlying request, which Huma's Context does not expose.
func hasBody(ctx huma.Context) bool {
	u, ok := ctx.(interface {
		Unwrap() (*http.Request, http.ResponseWriter)
	})
	if !ok {
		return false
	}
	r, _ := u.Unwrap()
	return r.ContentLength != 0
}
