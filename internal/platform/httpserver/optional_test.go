package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

type patchIn struct {
	Body struct {
		Name  Optional[string] `json:"name,omitempty"`
		Count Optional[int]    `json:"count,omitempty"`
	}
}

type patchOut struct {
	Body struct {
		NameSet    bool    `json:"name_set"`
		NameValue  *string `json:"name_value"`
		CountSet   bool    `json:"count_set"`
		CountValue *int    `json:"count_value"`
	}
}

func newPatchAPI(t *testing.T) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(problem.WithRequestID(req.Context(), "req-1")))
		})
	})
	api := NewAPI(r)
	huma.Register(api, huma.Operation{OperationID: "patchThing", Method: http.MethodPatch, Path: "/thing"},
		func(_ context.Context, in *patchIn) (*patchOut, error) {
			out := &patchOut{}
			out.Body.NameSet = in.Body.Name.Set
			out.Body.NameValue = in.Body.Name.Value
			out.Body.CountSet = in.Body.Count.Set
			out.Body.CountValue = in.Body.Count.Value
			return out, nil
		})
	return r
}

func TestOptional_AbsentNullValueInABody(t *testing.T) {
	h := newPatchAPI(t)
	str := func(s string) *string { return &s }
	num := func(n int) *int { return &n }
	tests := []struct {
		name       string
		body       string
		wantNameS  bool
		wantNameV  *string
		wantCountS bool
		wantCountV *int
	}{
		{"absent", `{}`, false, nil, false, nil},
		{"null", `{"name":null,"count":null}`, true, nil, true, nil},
		{"value", `{"name":"x","count":3}`, true, str("x"), true, num(3)},
		{"zero value is a value", `{"name":"","count":0}`, true, str(""), true, num(0)},
		{"only one key", `{"count":7}`, false, nil, true, num(7)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(t, h, http.MethodPatch, "/thing", jsonCT, tt.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			var got struct {
				NameSet    bool    `json:"name_set"`
				NameValue  *string `json:"name_value"`
				CountSet   bool    `json:"count_set"`
				CountValue *int    `json:"count_value"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.NameSet != tt.wantNameS || !samePtr(got.NameValue, tt.wantNameV) {
				t.Errorf("name = set %v value %v, want set %v value %v", got.NameSet, got.NameValue, tt.wantNameS, tt.wantNameV)
			}
			if got.CountSet != tt.wantCountS || !sameIntPtr(got.CountValue, tt.wantCountV) {
				t.Errorf("count = set %v value %v, want set %v value %v", got.CountSet, got.CountValue, tt.wantCountS, tt.wantCountV)
			}
		})
	}
}

func TestOptional_WrongTypeIsA422(t *testing.T) {
	h := newPatchAPI(t)
	rec := request(t, h, http.MethodPatch, "/thing", jsonCT, `{"count":"three"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if p := decodeProblem(t, rec.Body.Bytes()); p["code"] != "validation_failed" {
		t.Errorf("code = %v", p["code"])
	}
}

func TestOptional_SchemaIsNullableT(t *testing.T) {
	reg := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	s := Optional[int]{}.Schema(reg)
	if !s.Nullable || s.Type != huma.TypeInteger {
		t.Fatalf("schema = %+v, want nullable integer", s)
	}
	str := Optional[string]{}.Schema(reg)
	if !str.Nullable || str.Type != huma.TypeString {
		t.Fatalf("schema = %+v, want nullable string", str)
	}
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
