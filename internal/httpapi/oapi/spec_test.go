package oapi_test

import (
	"context"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// apiRoutes lists every endpoint of design-docs/API.md §5 (health included).
// The spec must keep covering all of them, each with an operationId.
var apiRoutes = []struct{ method, path string }{
	{"GET", "/healthz"},
	{"GET", "/readyz"},
	{"GET", "/api/v1/settings"},
	{"PATCH", "/api/v1/settings"},
	{"GET", "/api/v1/types"},
	{"POST", "/api/v1/types"},
	{"PATCH", "/api/v1/types/{key}"},
	{"DELETE", "/api/v1/types/{key}"},
	{"POST", "/api/v1/types/{key}/fields"},
	{"PUT", "/api/v1/types/{key}/fields/order"},
	{"GET", "/api/v1/types/{key}/fields/{fieldKey}"},
	{"PATCH", "/api/v1/types/{key}/fields/{fieldKey}"},
	{"DELETE", "/api/v1/types/{key}/fields/{fieldKey}"},
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

func loadSpec(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromFile("../../../api/openapi.yaml")
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return doc
}

func TestSpecIsValid(t *testing.T) {
	if err := loadSpec(t).Validate(context.Background()); err != nil {
		t.Fatalf("spec invalid: %v", err)
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
