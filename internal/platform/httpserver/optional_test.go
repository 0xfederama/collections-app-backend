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

// A wrong JSON type is the decoder's answer (400), not a field rule (422).
func TestOptional_WrongTypeIsA400(t *testing.T) {
	h := newPatchAPI(t)
	rec := request(t, h, http.MethodPatch, "/thing", jsonCT, `{"count":"three"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if p := decodeProblem(t, rec.Body.Bytes()); p["code"] != "malformed_request" {
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

// A struct T is inlined, never a $ref: its schema is a nullable object (type
// [object, null] in 3.1), so the optional form needs no oneOf and no nullable $ref.
func TestOptional_SchemaOfAStructIsAnInlineNullableObject(t *testing.T) {
	type inner struct {
		Amount string `json:"amount"`
	}
	reg := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	s := Optional[inner]{}.Schema(reg)
	if s.Ref != "" || len(s.OneOf) != 0 {
		t.Fatalf("schema = %+v, want an inline object", s)
	}
	if !s.Nullable || s.Type != huma.TypeObject {
		t.Fatalf("schema = %+v, want a nullable object", s)
	}
	if _, ok := s.Properties["amount"]; !ok {
		t.Errorf("properties = %v, want amount", s.Properties)
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

// A struct-valued Optional is validated at run time: an object and null are
// accepted, and a wrong JSON type is the decoder's answer (400), as before.
func TestOptional_StructValueAndNullValidate(t *testing.T) {
	type inner struct {
		Amount string `json:"amount"`
	}
	type structPatchIn struct {
		Body struct {
			Price Optional[inner] `json:"price,omitempty"`
		}
	}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(problem.WithRequestID(req.Context(), "req-1")))
		})
	})
	api := NewAPI(r)
	huma.Register(api, huma.Operation{OperationID: "patchPrice", Method: http.MethodPatch, Path: "/price"},
		func(_ context.Context, in *structPatchIn) (*struct{}, error) { return &struct{}{}, nil })

	for _, tt := range []struct {
		name   string
		body   string
		status int
	}{
		{"absent", `{}`, http.StatusNoContent},
		{"null", `{"price":null}`, http.StatusNoContent},
		{"object", `{"price":{"amount":"12.00"}}`, http.StatusNoContent},
		{"a string is a wrong type (400)", `{"price":"12.00"}`, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(t, r, http.MethodPatch, "/price", jsonCT, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body)
			}
		})
	}
}
