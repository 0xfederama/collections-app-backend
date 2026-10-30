package httpserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
)

// newIfMatchAPI mounts one PUT that requires an item ETag, behind IfMatchFirst.
func newIfMatchAPI(t *testing.T) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	api := NewAPI(r)
	huma.Register(api, huma.Operation{
		OperationID: "putItem", Method: http.MethodPut, Path: "/item",
		Middlewares: huma.Middlewares{IfMatchFirst(api, etag.Item)},
	}, func(_ context.Context, _ *struct{}) (*struct{}, error) {
		return &struct{}{}, nil
	})
	return r
}

func TestIfMatchFirst(t *testing.T) {
	h := newIfMatchAPI(t)
	tests := []struct {
		name     string
		ifMatch  string // "" = header absent
		wantCode int
	}{
		{"missing If-Match is 428", "", http.StatusPreconditionRequired},
		{"malformed If-Match is 428", "i-3", http.StatusPreconditionRequired},
		{"unquoted version is 428", "3", http.StatusPreconditionRequired},
		{"weak validator is 428", `W/"i-3"`, http.StatusPreconditionRequired},
		{"another resource's ETag is 428", etag.Format(etag.Folder, 3), http.StatusPreconditionRequired},
		{"valid ETag passes", etag.Format(etag.Item, 3), http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var headers []string
			if tt.ifMatch != "" {
				headers = []string{"If-Match", tt.ifMatch}
			}
			rec := request(t, h, http.MethodPut, "/item", "", "", headers...)
			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantCode, rec.Body)
			}
			if tt.wantCode == http.StatusPreconditionRequired {
				if p := decodeProblem(t, rec.Body.Bytes()); p["code"] != "precondition_required" {
					t.Errorf("code = %v, want precondition_required", p["code"])
				}
			}
		})
	}
}
