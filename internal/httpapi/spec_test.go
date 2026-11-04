package httpapi_test

import (
	"context"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/getkin/kin-openapi/openapi3"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
)

// apiRoutes lists every endpoint of design-docs/API.md §5 (health included).
// The spec must keep covering all of them, each with an operationId.
var apiRoutes = []struct{ method, path string }{
	{"GET", "/healthz"},
	{"GET", "/readyz"},
	{"GET", "/api/v1/settings"},
	{"PATCH", "/api/v1/settings"},
	{"GET", "/api/v1/store-providers"},
	{"GET", "/api/v1/books/suggestions"},
	{"GET", "/api/v1/types"},
	{"GET", "/api/v1/tmdb/suggestions"},
	{"GET", "/api/v1/tree"},
	{"POST", "/api/v1/folders"},
	{"GET", "/api/v1/folders/{id}"},
	{"PATCH", "/api/v1/folders/{id}"},
	{"DELETE", "/api/v1/folders/{id}"},
	{"POST", "/api/v1/folders/{id}/move"},
	{"PUT", "/api/v1/folders/order"},
	{"POST", "/api/v1/lists"},
	{"GET", "/api/v1/lists/{id}"},
	{"PATCH", "/api/v1/lists/{id}"},
	{"DELETE", "/api/v1/lists/{id}"},
	{"POST", "/api/v1/lists/{id}/move"},
	{"PUT", "/api/v1/lists/order"},
	{"GET", "/api/v1/lists/{id}/items"},
	{"POST", "/api/v1/lists/{id}/items"},
	{"GET", "/api/v1/items/{id}"},
	{"PATCH", "/api/v1/items/{id}"},
	{"DELETE", "/api/v1/items/{id}"},
	{"POST", "/api/v1/items/{id}/lookup"},
	{"POST", "/api/v1/lists/{id}/lookup-refresh"},
	{"POST", "/api/v1/items/{id}/move"},
	{"PUT", "/api/v1/lists/{id}/items/order"},
	{"PUT", "/api/v1/items/{id}/image"},
	{"DELETE", "/api/v1/items/{id}/image"},
	{"GET", "/api/v1/attachments/{id}/file"},
	{"GET", "/api/v1/attachments/{id}/thumbnail"},
	{"GET", "/api/v1/items/{id}/content"},
	{"PUT", "/api/v1/items/{id}/content"},
	{"GET", "/api/v1/items/{id}/content/versions"},
	{"GET", "/api/v1/items/{id}/content/versions/{version}"},
	{"POST", "/api/v1/items/{id}/content/restore"},
	{"GET", "/api/v1/search"},
	{"GET", "/api/v1/recent-items"},
	{"GET", "/api/v1/trash"},
	{"DELETE", "/api/v1/trash"},
	{"POST", "/api/v1/trash/{batch_id}/restore"},
	{"DELETE", "/api/v1/trash/{batch_id}"},
}

// The contract is generated from the registered operations (cmd/specgen writes
// api/openapi.yaml); these tests read the committed file and compare it with
// the registrations. Redocly lints the file (make lint-api).
const specFile = "../../api/openapi.yaml"

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile(specFile)
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return doc
}

// TestSpecIsValid checks the committed file with kin-openapi. Its examples are
// not checked here: the generated spec has none, and Redocly covers the rest.
func TestSpecIsValid(t *testing.T) {
	if err := loadSpec(t).Validate(context.Background()); err != nil {
		t.Fatalf("spec invalid: %v", err)
	}
}

// TestSpecMatchesRegistrations: the committed file lists every operation the
// API registers, with the same method, path and operationId, and nothing else.
func TestSpecMatchesRegistrations(t *testing.T) {
	registered := httpapi.API(httpapi.Deps{}).OpenAPI()
	doc := loadSpec(t)
	want := map[string]string{}
	for path, item := range registered.Paths {
		for method, op := range registeredOperations(item) {
			want[method+" "+path] = op.OperationID
		}
	}
	got := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			got[method+" "+path] = op.OperationID
		}
	}
	for key, id := range want {
		if gotID, ok := got[key]; !ok {
			t.Errorf("%s is registered but missing from %s (run make spec)", key, specFile)
		} else if gotID != id {
			t.Errorf("%s: operationId %q in %s, %q registered", key, gotID, specFile, id)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s is in %s but not registered (run make spec)", key, specFile)
		}
	}
}

// registeredOperations maps the methods of a Huma path item to their operations.
func registeredOperations(item *huma.PathItem) map[string]*huma.Operation {
	out := map[string]*huma.Operation{}
	for method, op := range map[string]*huma.Operation{
		"GET": item.Get, "PUT": item.Put, "POST": item.Post, "DELETE": item.Delete,
		"PATCH": item.Patch, "HEAD": item.Head, "OPTIONS": item.Options, "TRACE": item.Trace,
	} {
		if op != nil {
			out[method] = op
		}
	}
	return out
}

// TestSpecOperationsAreDocumented: every operation has a unique operationId, a
// summary, tags and at least one response.
func TestSpecOperationsAreDocumented(t *testing.T) {
	seen := map[string]string{}
	for path, item := range loadSpec(t).Paths.Map() {
		for method, op := range item.Operations() {
			key := method + " " + path
			if op.OperationID == "" {
				t.Errorf("%s: missing operationId", key)
			}
			if prev, dup := seen[op.OperationID]; dup {
				t.Errorf("%s: operationId %q already used by %s", key, op.OperationID, prev)
			}
			seen[op.OperationID] = key
			if op.Summary == "" {
				t.Errorf("%s: missing summary", key)
			}
			if len(op.Tags) == 0 {
				t.Errorf("%s: missing tags", key)
			}
			if op.Responses == nil || op.Responses.Len() == 0 {
				t.Errorf("%s: no responses", key)
			}
		}
	}
}

func TestSpecCoversAllAPIRoutes(t *testing.T) {
	doc := loadSpec(t)
	seen := map[string]string{}
	for _, rt := range apiRoutes {
		item := doc.Paths.Value(rt.path)
		if item == nil {
			t.Errorf("%s %s: path missing from spec", rt.method, rt.path)
			continue
		}
		op := item.GetOperation(rt.method)
		if op == nil {
			t.Errorf("%s %s: operation missing from spec", rt.method, rt.path)
			continue
		}
		if op.OperationID == "" {
			t.Errorf("%s %s: missing operationId", rt.method, rt.path)
		}
		if prev, dup := seen[op.OperationID]; dup {
			t.Errorf("%s %s: operationId %q already used by %s", rt.method, rt.path, op.OperationID, prev)
		}
		seen[op.OperationID] = rt.method + " " + rt.path
	}

	// The reverse direction: no operation without an API.md row.
	want := map[string]bool{}
	for _, rt := range apiRoutes {
		want[rt.method+" "+rt.path] = true
	}
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			if !want[method+" "+path] {
				t.Errorf("%s %s is in the spec but not in apiRoutes (update the list and API.md)", method, path)
			}
		}
	}
}
