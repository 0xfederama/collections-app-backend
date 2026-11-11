package httpapi

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/danielgtaylor/huma/v2"
)

var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// TestGuardCoversEveryPathParam walks every registered operation and fails when one has a path
// parameter the access guard has no rule for, so a new endpoint cannot skip the visibility check.
func TestGuardCoversEveryPathParam(t *testing.T) {
	spec := API(Deps{}).OpenAPI()
	seen := 0
	for path, item := range spec.Paths {
		params := pathParam.FindAllStringSubmatch(path, -1)
		if len(params) == 0 {
			continue
		}
		seen++
		rule, ok := ruleFor(path)
		if !ok {
			t.Errorf("%s has path parameters but no guard rule: add one to guardRules (and a kind to access)", path)
			continue
		}
		for _, m := range params {
			if m[1] != rule.param && !plainParams[m[1]] {
				t.Errorf("%s: parameter {%s} is neither the guarded {%s} nor a plain parameter", path, m[1], rule.param)
			}
		}
		for method, op := range map[string]*huma.Operation{http.MethodGet: item.Get, http.MethodPut: item.Put,
			http.MethodPost: item.Post, http.MethodDelete: item.Delete, http.MethodPatch: item.Patch} {
			if op != nil && op.Path != "" && op.Path != path {
				t.Errorf("%s %s: operation path differs", method, path)
			}
		}
	}
	if seen < 10 {
		t.Fatalf("only %d parameterized paths found: the walk is not seeing the API", seen)
	}
}

func TestRuleForMatchesWholeSegments(t *testing.T) {
	for _, tt := range []struct {
		path string
		ok   bool
	}{
		{"/api/v1/folders/{id}", true},
		{"/api/v1/folders/{id}/move", true},
		{"/api/v1/folders/order", false},
		{"/api/v1/lists/{id}/items/order", true},
		{"/api/v1/items/{id}/content/versions/{version}", true},
		{"/api/v1/attachments/{id}/thumbnail", true},
		{"/api/v1/trash/{batch_id}/restore", true},
		{"/api/v1/trash", false},
		{"/api/v1/folders/{idx}", false},
	} {
		if _, ok := ruleFor(tt.path); ok != tt.ok {
			t.Errorf("ruleFor(%q) = %v, want %v", tt.path, ok, tt.ok)
		}
	}
}
