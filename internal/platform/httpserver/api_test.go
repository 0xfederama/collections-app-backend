package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

type createItemIn struct {
	Body struct {
		Title string        `json:"title" minLength:"1" doc:"Item title"`
		Count Optional[int] `json:"count,omitempty"`
	}
}

type itemOut struct {
	Body struct {
		Title string `json:"title"`
	}
}

type getItemIn struct {
	ID    int `path:"id"`
	N     int `query:"n"`
	Limit int `header:"X-Limit"`
}

type getItemOut struct {
	Body struct {
		ID int `json:"id"`
	}
}

type uploadIn struct {
	Body struct {
		Title string `json:"title"`
	}
}

type failIn struct {
	Kind string `query:"kind"`
}

// newTestAPI is a tiny API with one POST and one GET, plus an operation that
// raises the body limit and one that fails on demand. Every request carries the
// request id "req-1".
func newTestAPI(t *testing.T) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(problem.WithRequestID(req.Context(), "req-1")))
		})
	})
	api := NewAPI(r)
	huma.Register(api, huma.Operation{OperationID: "createItem", Method: http.MethodPost, Path: "/items", DefaultStatus: http.StatusCreated},
		func(_ context.Context, in *createItemIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.Title = in.Body.Title
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "getItem", Method: http.MethodGet, Path: "/items/{id}"},
		func(_ context.Context, in *getItemIn) (*getItemOut, error) {
			out := &getItemOut{}
			out.Body.ID = in.ID
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "upload", Method: http.MethodPost, Path: "/upload",
		DefaultStatus: http.StatusCreated, MaxBodyBytes: 2 << 20},
		func(_ context.Context, in *uploadIn) (*itemOut, error) {
			out := &itemOut{}
			out.Body.Title = in.Body.Title
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "fail", Method: http.MethodGet, Path: "/fail"},
		func(_ context.Context, in *failIn) (*getItemOut, error) {
			switch in.Kind {
			case "notfound":
				return nil, apperr.NotFound("item")
			case "internal":
				return nil, errors.New(`pq: relation "items" does not exist SELECT * FROM items`)
			}
			return &getItemOut{}, nil
		})
	return r
}

func request(t *testing.T, h http.Handler, method, path, contentType, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const jsonCT = "application/json"

func TestRules_ErrorsAreProblems(t *testing.T) {
	h := newTestAPI(t)
	big := `{"title":"` + strings.Repeat("a", DefaultMaxBodyBytes) + `"}`
	tests := []struct {
		name       string
		method     string
		path       string
		contentTyp string
		body       string
		headers    []string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		// Rule 1: an undeclared body key is 400 unknown_field.
		{"unknown body key", http.MethodPost, "/items", jsonCT, `{"title":"x","bogus":1}`, nil,
			400, "unknown_field", `Unknown field "bogus".`},
		// Rule 2: a bad path, query or header value is 400 invalid_parameter.
		{"bad path value", http.MethodGet, "/items/abc", "", "", nil,
			400, "invalid_parameter", `"id" is invalid.`},
		{"bad query value", http.MethodGet, "/items/1?n=abc", "", "", nil,
			400, "invalid_parameter", `"n" is invalid.`},
		{"bad header value", http.MethodGet, "/items/1", "", "", []string{"X-Limit", "abc"},
			400, "invalid_parameter", `"X-Limit" is invalid.`},
		// Rule 3: a wrong or missing Content-Type is 400, the body is never echoed.
		{"wrong content type", http.MethodPost, "/items", "text/plain", "hello body", nil,
			400, "malformed_request", "Unsupported Content-Type."},
		{"body without content type", http.MethodPost, "/items", "", `{"title":"x"}`, nil,
			400, "malformed_request", "Unsupported Content-Type."},
		{"body that is not JSON", http.MethodPost, "/items", jsonCT, `{"title":`, nil,
			400, "malformed_request", "The request body is not valid JSON."},
		{"body over the default limit", http.MethodPost, "/items", jsonCT, big, nil,
			413, "payload_too_large", "The request body exceeds the limit for this route."},
		{"missing required field", http.MethodPost, "/items", jsonCT, `{}`, nil,
			422, "validation_failed", "Invalid field: title (expected required property title to be present)."},
		// Rule 4 (the handler paths): domain errors and unknown errors.
		{"domain error from a handler", http.MethodGet, "/fail?kind=notfound", "", "", nil,
			404, "not_found", "Item not found."},
		{"unknown error from a handler", http.MethodGet, "/fail?kind=internal", "", "", nil,
			500, "internal", "Something went wrong."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := request(t, h, tt.method, tt.path, tt.contentTyp, tt.body, tt.headers...)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			p := decodeProblem(t, rec.Body.Bytes())
			if p["code"] != tt.wantCode {
				t.Errorf("code = %v, want %s", p["code"], tt.wantCode)
			}
			if p["detail"] != tt.wantDetail {
				t.Errorf("detail = %v, want %q", p["detail"], tt.wantDetail)
			}
			if p["status"] != float64(tt.wantStatus) {
				t.Errorf("status member = %v", p["status"])
			}
			if strings.Contains(rec.Body.String(), "hello body") || strings.Contains(rec.Body.String(), "SELECT") {
				t.Errorf("body leaks request or internal text: %s", rec.Body)
			}
		})
	}
}

func TestRules_RequestIDOnlyOnInternal(t *testing.T) {
	h := newTestAPI(t)
	internal := decodeProblem(t, request(t, h, http.MethodGet, "/fail?kind=internal", "", "").Body.Bytes())
	if internal["request_id"] != "req-1" {
		t.Errorf("internal request_id = %v, want req-1", internal["request_id"])
	}
	for _, path := range []string{"/fail?kind=notfound", "/items/abc"} {
		p := decodeProblem(t, request(t, h, http.MethodGet, path, "", "").Body.Bytes())
		if _, ok := p["request_id"]; ok {
			t.Errorf("%s: request_id set on a non-internal problem: %v", path, p)
		}
	}
}

func TestRules_OperationOverridesBodyLimit(t *testing.T) {
	h := newTestAPI(t)
	// 100 KiB is over the default 64 KiB but under the upload's own limit.
	body := `{"title":"` + strings.Repeat("a", 100<<10) + `"}`
	rec := request(t, h, http.MethodPost, "/upload", jsonCT, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if rec2 := request(t, h, http.MethodPost, "/items", jsonCT, body); rec2.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default route status = %d, want 413", rec2.Code)
	}
}

func TestRules_DefaultBodyLimitIs64KiB(t *testing.T) {
	if DefaultMaxBodyBytes != 64<<10 {
		t.Fatalf("DefaultMaxBodyBytes = %d", DefaultMaxBodyBytes)
	}
}

func TestRules_ValidRequestIsNotAProblem(t *testing.T) {
	h := newTestAPI(t)
	rec := request(t, h, http.MethodPost, "/items", jsonCT, `{"title":"x"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["title"] != "x" {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "problem") {
		t.Errorf("Content-Type = %q on a success", ct)
	}
}

func TestRules_GetWithoutBodyNeedsNoContentType(t *testing.T) {
	h := newTestAPI(t)
	rec := request(t, h, http.MethodGet, "/items/7", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestRules_FieldErrorsNameTheField(t *testing.T) {
	h := newTestAPI(t)
	rec := request(t, h, http.MethodPost, "/items", jsonCT, `{"title":""}`)
	p := decodeProblem(t, rec.Body.Bytes())
	errs, ok := p["errors"].([]any)
	if rec.Code != http.StatusUnprocessableEntity || !ok || len(errs) == 0 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	first := errs[0].(map[string]any)
	if first["field"] != "title" {
		t.Errorf("field = %v, want title", first["field"])
	}
}

func decodeProblem(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a JSON problem: %s (%v)", body, err)
	}
	return p
}
