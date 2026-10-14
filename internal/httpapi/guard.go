package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/etag"
)

// Body limits (API.md §6).
const (
	jsonBodyLimit      = 64 << 10
	contentBodyLimit   = 2 << 20
	multipartBodyLimit = 10<<20 + 64<<10

	// uploadTimeout replaces the server-wide read/write deadline for multipart uploads.
	uploadTimeout = 2 * time.Minute
)

// ifMatchPrefix lists the operations that require `If-Match` and the ETag
// prefix each expects (API.md §3 "Requires If-Match"). AT-10 checks it against
// an independent list written from API.md.
var ifMatchPrefix = map[string]string{
	"UpdateFolder":          etag.Folder,
	"DeleteFolder":          etag.Folder,
	"MoveFolder":            etag.Folder,
	"UpdateList":            etag.List,
	"DeleteList":            etag.List,
	"MoveList":              etag.List,
	"UpdateItem":            etag.Item,
	"DeleteItem":            etag.Item,
	"MoveItem":              etag.Item,
	"SetItemImage":          etag.Item,
	"DeleteItemImage":       etag.Item,
	"UpdateItemContent":     etag.Content,
	"RestoreContentVersion": etag.Content,
	"UpdateSettings":        etag.Settings,
}

// operation is what the guard needs to know about one spec operation.
type operation struct {
	id          string
	bodies      map[string]*openapi3.Schema // request body schema per media type
	queryParams []*openapi3.Parameter
}

// guard enforces, once and centrally, the request rules the generated strict
// server skips: query constraints, If-Match, body limits and unknown fields.
// It runs after routing and path binding, before the body is decoded.
type guard struct{ ops map[string]*operation }

func newGuard(doc *openapi3.T) *guard {
	g := &guard{ops: map[string]*operation{}}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			o := &operation{id: op.OperationID, bodies: map[string]*openapi3.Schema{}}
			for _, p := range op.Parameters {
				if p.Value != nil && p.Value.In == openapi3.ParameterInQuery {
					o.queryParams = append(o.queryParams, p.Value)
				}
			}
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				for mt, c := range op.RequestBody.Value.Content {
					if c.Schema != nil {
						o.bodies[mt] = c.Schema.Value
					}
				}
			}
			g.ops[method+" "+path] = o
		}
	}
	return g
}

func (g *guard) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.RouteContext(r.Context())
		op := g.ops[r.Method+" "+rctx.RoutePattern()]
		if op == nil {
			next.ServeHTTP(w, r)
			return
		}
		if p, ok := op.checkQuery(r); !ok {
			writeProblem(w, p)
			return
		}
		if prefix, required := ifMatchPrefix[op.id]; required {
			version, ok := etag.Parse(singleHeader(r, "If-Match"), prefix)
			if !ok {
				writeProblem(w, preconditionRequired())
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), ctxIfMatchVersion, version))
		}
		if len(op.bodies) > 0 {
			var p problem
			if r, p = op.checkBody(w, r); p.Code != "" {
				writeProblem(w, p)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// singleHeader returns the header value, or "" when it is absent or repeated.
func singleHeader(r *http.Request, name string) string {
	v := r.Header.Values(name)
	if len(v) != 1 {
		return ""
	}
	return v[0]
}

// checkQuery applies the spec's query parameter schemas (ranges, enums,
// lengths), which the generated binder does not.
func (o *operation) checkQuery(r *http.Request) (problem, bool) {
	input := &openapi3filter.RequestValidationInput{Request: r, Options: &openapi3filter.Options{SkipSettingDefaults: true}}
	for _, param := range o.queryParams {
		if !r.URL.Query().Has(param.Name) {
			continue // required-ness is the generated binder's job
		}
		if err := openapi3filter.ValidateParameter(r.Context(), input, param); err != nil {
			return invalidParameter(fmt.Sprintf("%q is invalid.", param.Name)), false
		}
	}
	return problem{}, true
}

// bodyLimit returns the byte limit for a request body (API.md §6).
func bodyLimit(opID, mediaType string) int64 {
	switch {
	case mediaType == "multipart/form-data":
		return multipartBodyLimit
	case opID == "UpdateItemContent":
		return contentBodyLimit
	default:
		return jsonBodyLimit
	}
}

// checkBody enforces the media type, the size limit and (for JSON) the
// unknown-field rule. It returns the request with a replayable body.
func (o *operation) checkBody(w http.ResponseWriter, r *http.Request) (*http.Request, problem) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	schema, declared := o.bodies[mediaType]
	if err != nil || !declared {
		if r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
			return r, problem{} // no body: the strict handler answers 400 malformed_request
		}
		return r, malformedRequest("Unsupported Content-Type.")
	}

	limit := bodyLimit(o.id, mediaType)
	if r.ContentLength > limit {
		return r, payloadTooLarge()
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	if mediaType == "multipart/form-data" {
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(uploadTimeout)) // ErrNotSupported on test recorders: ignore
		_ = rc.SetWriteDeadline(time.Now().Add(uploadTimeout))
		return r, problem{}
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return r, payloadTooLarge()
		}
		return r, malformedRequest("The request body could not be read.")
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))

	var doc any
	if json.Unmarshal(raw, &doc) != nil {
		return r, problem{} // not JSON: the strict handler answers 400 malformed_request
	}
	if path, found := findUnknownField(schema, doc, ""); found {
		return r, unknownField(path)
	}
	return r, problem{}
}

// findUnknownField walks a decoded JSON value against its schema and reports
// the first property the schema does not declare (sorted order, so the answer
// is deterministic). Objects without declared properties are free-form
// (`metadata`, validated per kind later) and are not inspected.
func findUnknownField(s *openapi3.Schema, v any, path string) (string, bool) {
	if s == nil {
		return "", false
	}
	switch val := v.(type) {
	case map[string]any:
		props := declaredProperties(s)
		if len(props) == 0 || len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
			return "", false
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			prop, ok := props[k]
			if !ok {
				if allowsAdditional(s) {
					continue
				}
				return joinPath(path, k), true
			}
			if p, found := findUnknownField(prop.Value, val[k], joinPath(path, k)); found {
				return p, true
			}
		}
	case []any:
		if s.Items == nil {
			return "", false
		}
		for i, el := range val {
			if p, found := findUnknownField(s.Items.Value, el, fmt.Sprintf("%s[%d]", path, i)); found {
				return p, true
			}
		}
	}
	return "", false
}

// declaredProperties merges the properties of s and of its allOf members.
func declaredProperties(s *openapi3.Schema) openapi3.Schemas {
	if len(s.AllOf) == 0 {
		return s.Properties
	}
	merged := openapi3.Schemas{}
	for k, v := range s.Properties {
		merged[k] = v
	}
	for _, sub := range s.AllOf {
		if sub.Value == nil {
			continue
		}
		for k, v := range declaredProperties(sub.Value) {
			merged[k] = v
		}
	}
	return merged
}

func allowsAdditional(s *openapi3.Schema) bool {
	ap := s.AdditionalProperties
	return ap.Schema != nil || (ap.Has != nil && *ap.Has)
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}
