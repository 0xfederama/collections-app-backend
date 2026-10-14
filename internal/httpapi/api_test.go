package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
)

const (
	id1     = "0192f4a0-1111-7000-8000-000000000001"
	id2     = "0192f4a0-2222-7000-8000-000000000002"
	jsonCT  = "application/json"
	bigBody = "x"
)

func stubRouter(t *testing.T) http.Handler {
	t.Helper()
	return newRouter(t, httpapi.Deps{DB: fakePinger{}, DataDir: t.TempDir(), Logger: quietLogger()})
}

type req struct {
	method, path, body, contentType string
	headers                         map[string]string
}

func do(t *testing.T, h http.Handler, r req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequestWithContext(t.Context(), r.method, r.path, body)
	if r.contentType != "" {
		hr.Header.Set("Content-Type", r.contentType)
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, hr)
	return rec
}

type problemBody struct {
	Type           string `json:"type"`
	Title          string `json:"title"`
	Status         int    `json:"status"`
	Detail         string `json:"detail"`
	Code           string `json:"code"`
	RequestID      string `json:"request_id"`
	CurrentVersion *int   `json:"current_version"`
	Errors         []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
	BlockingParent *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"blocking_parent"`
}

// wantProblem asserts status, problem+json content type and code, and returns the body.
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) problemBody {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p problemBody
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body)
	}
	if p.Code != code || p.Status != status || p.Type == "" || p.Title == "" {
		t.Fatalf("problem = %+v, want code %q status %d with type and title", p, code, status)
	}
	return p
}

// TestAT10_IfMatchRequired covers every operation of API.md §3 "Requires
// If-Match": PATCH and DELETE on folders/lists/items, all …/move, PUT …/content,
// POST …/content/restore, PUT and DELETE …/image, PATCH /settings.
func TestAT10_IfMatchRequired(t *testing.T) {
	h := stubRouter(t)
	ops := []struct {
		name, method, path, body, prefix string
	}{
		{"UpdateFolder", "PATCH", "/api/v1/folders/" + id1, `{"name":"n"}`, "f"},
		{"DeleteFolder", "DELETE", "/api/v1/folders/" + id1, "", "f"},
		{"MoveFolder", "POST", "/api/v1/folders/" + id1 + "/move", `{"parent_folder_id":null}`, "f"},
		{"UpdateList", "PATCH", "/api/v1/lists/" + id1, `{"name":"n"}`, "l"},
		{"DeleteList", "DELETE", "/api/v1/lists/" + id1, "", "l"},
		{"MoveList", "POST", "/api/v1/lists/" + id1 + "/move", `{"folder_id":null}`, "l"},
		{"UpdateItem", "PATCH", "/api/v1/items/" + id1, `{"title":"t"}`, "i"},
		{"DeleteItem", "DELETE", "/api/v1/items/" + id1, "", "i"},
		{"MoveItem", "POST", "/api/v1/items/" + id1 + "/move", `{"list_id":"` + id2 + `"}`, "i"},
		{"UpdateItemContent", "PUT", "/api/v1/items/" + id1 + "/content", `{"body":"hello"}`, "c"},
		{"RestoreContentVersion", "POST", "/api/v1/items/" + id1 + "/content/restore", `{"version":1}`, "c"},
		{"SetItemImage", "PUT", "/api/v1/items/" + id1 + "/image", `{"url":"https://img.example/a.png"}`, "i"},
		{"DeleteItemImage", "DELETE", "/api/v1/items/" + id1 + "/image", "", "i"},
		{"UpdateSettings", "PATCH", "/api/v1/settings", `{"workspace_name":"n"}`, "s"},
	}
	for _, op := range ops {
		ct := ""
		if op.body != "" {
			ct = jsonCT
		}
		call := func(ifMatch *string) *httptest.ResponseRecorder {
			r := req{method: op.method, path: op.path, body: op.body, contentType: ct}
			if ifMatch != nil {
				r.headers = map[string]string{"If-Match": *ifMatch}
			}
			return do(t, h, r)
		}
		str := func(s string) *string { return &s }

		t.Run(op.name+"/missing", func(t *testing.T) { wantProblem(t, call(nil), 428, "precondition_required") })
		bad := []string{"*", `"` + op.prefix + `-x"`, `"` + op.prefix + `-"`, `"` + op.prefix + `-01"`, `"` + op.prefix + `-1`, `W/"` + op.prefix + `-1"`,
			`"z-1"`, `"` + op.prefix + `-1", "` + op.prefix + `-2"`, `"` + op.prefix + `--1"`, `"` + op.prefix + `-99999999999999999999"`, ""}
		for i, v := range bad {
			t.Run(op.name+"/rejects/"+strconv.Itoa(i), func(t *testing.T) { wantProblem(t, call(str(v)), 428, "precondition_required") })
		}
		for _, other := range []string{"f", "l", "i", "c", "s"} {
			if other == op.prefix {
				continue
			}
			t.Run(op.name+"/wrong-prefix-"+other, func(t *testing.T) {
				wantProblem(t, call(str(`"`+other+`-1"`)), 428, "precondition_required")
			})
		}
		if op.name == "UpdateSettings" {
			continue // implemented (B-05): settings_test.go covers a valid If-Match
		}
		t.Run(op.name+"/valid-reaches-handler", func(t *testing.T) {
			rec := call(str(`"` + op.prefix + `-1"`))
			wantProblem(t, rec, 501, "not_implemented")
		})
	}
}

// TestIfMatchNotRequired checks the operations API.md §3 exempts (creates,
// …/order, trash restore and purge) are not blocked by the guard.
func TestIfMatchNotRequired(t *testing.T) {
	h := stubRouter(t)
	for _, r := range []req{
		{method: "POST", path: "/api/v1/folders", body: `{"name":"n"}`, contentType: jsonCT},
		{method: "POST", path: "/api/v1/lists", body: `{"name":"n"}`, contentType: jsonCT},
		{method: "POST", path: "/api/v1/lists/" + id1 + "/items", body: `{"title":"t"}`, contentType: jsonCT},
		{method: "PUT", path: "/api/v1/folders/order", body: `{"parent_folder_id":null,"ids":[]}`, contentType: jsonCT},
		{method: "PUT", path: "/api/v1/lists/order", body: `{"folder_id":null,"ids":[]}`, contentType: jsonCT},
		{method: "PUT", path: "/api/v1/lists/" + id1 + "/items/order", body: `{"ids":[]}`, contentType: jsonCT},
		{method: "POST", path: "/api/v1/trash/" + id1 + "/restore"},
		{method: "DELETE", path: "/api/v1/trash/" + id1},
		{method: "GET", path: "/api/v1/tree"},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			wantProblem(t, do(t, h, r), 501, "not_implemented")
		})
	}
}

func TestUnstubbedRoutesAnswer501(t *testing.T) {
	rec := do(t, stubRouter(t), req{method: "GET", path: "/api/v1/tree"})
	p := wantProblem(t, rec, 501, "not_implemented")
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("missing X-Request-Id")
	}
	if p.Type != "https://collection-app.invalid/problems/not-implemented" {
		t.Errorf("type = %q", p.Type)
	}
}

func TestBadRequests(t *testing.T) {
	h := stubRouter(t)
	items := "/api/v1/lists/" + id1 + "/items"
	tests := []struct {
		name       string
		r          req
		wantCode   string
		wantDetail string // substring
	}{
		{"unknown top-level field", req{method: "POST", path: "/api/v1/folders", body: `{"name":"n","colour":"red"}`, contentType: jsonCT}, "unknown_field", `"colour"`},
		{"unknown nested field (price)", req{method: "POST", path: items, body: `{"title":"t","price":{"currency":"EUR","foo":1}}`, contentType: jsonCT}, "unknown_field", `"price.foo"`},
		{"unknown nested field (destination)", req{method: "POST", path: "/api/v1/trash/" + id1 + "/restore", body: `{"destination":{"bar":1}}`, contentType: jsonCT}, "unknown_field", `"destination.bar"`},
		{"unknown field in array element", req{method: "POST", path: "/api/v1/folders", body: `{"name":"n","extra":[1]}`, contentType: jsonCT}, "unknown_field", `"extra"`},
		{"unknown field on PATCH with If-Match", req{method: "PATCH", path: "/api/v1/items/" + id1, body: `{"titel":"t"}`, contentType: jsonCT, headers: map[string]string{"If-Match": `"i-1"`}}, "unknown_field", `"titel"`},
		{"malformed JSON", req{method: "POST", path: "/api/v1/folders", body: `{"name":`, contentType: jsonCT}, "malformed_request", ""},
		{"not an object", req{method: "POST", path: "/api/v1/folders", body: `[1]`, contentType: jsonCT}, "malformed_request", ""},
		{"empty body", req{method: "POST", path: "/api/v1/folders", contentType: jsonCT}, "malformed_request", ""},
		{"wrong content type", req{method: "POST", path: "/api/v1/folders", body: `{"name":"n"}`, contentType: "text/plain"}, "malformed_request", "Content-Type"},
		{"limit too large", req{method: "GET", path: items + "?limit=500"}, "invalid_parameter", `"limit"`},
		{"limit not a number", req{method: "GET", path: items + "?limit=abc"}, "invalid_parameter", `"limit"`},
		{"limit zero", req{method: "GET", path: items + "?limit=0"}, "invalid_parameter", `"limit"`},
		{"negative offset", req{method: "GET", path: items + "?offset=-1"}, "invalid_parameter", `"offset"`},
		{"unknown sort", req{method: "GET", path: items + "?sort=random"}, "invalid_parameter", `"sort"`},
		{"search without q", req{method: "GET", path: "/api/v1/search"}, "invalid_parameter", `"q"`},
		{"search with empty q", req{method: "GET", path: "/api/v1/search?q="}, "invalid_parameter", `"q"`},
		{"search with long q", req{method: "GET", path: "/api/v1/search?q=" + strings.Repeat("a", 201)}, "invalid_parameter", `"q"`},
		{"path id not a uuid", req{method: "GET", path: "/api/v1/folders/not-a-uuid"}, "invalid_parameter", `"id"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, do(t, h, tt.r), 400, tt.wantCode)
			if !strings.Contains(p.Detail, tt.wantDetail) {
				t.Errorf("detail = %q, want it to contain %q", p.Detail, tt.wantDetail)
			}
			if strings.Contains(p.Detail, "not-a-uuid") || strings.Contains(p.Detail, "abc") {
				t.Errorf("detail %q echoes the offending value", p.Detail)
			}
		})
	}
}

func TestGuardLetsValidRequestsThrough(t *testing.T) {
	h := stubRouter(t)
	items := "/api/v1/lists/" + id1 + "/items"
	for _, r := range []req{
		{method: "GET", path: items + "?limit=200&offset=0&sort=title&q=dune"},
		{method: "GET", path: "/api/v1/search?q=x&limit=1"},
		// metadata is free-form here; per-kind validation is B-07's job
		{method: "POST", path: items, body: `{"title":"t","metadata":{"anything":{"goes":[1,2]}},"price":{"currency":"EUR","min":"1.00","max":null}}`, contentType: jsonCT},
		{method: "POST", path: "/api/v1/folders", body: `{"name":"n"}`, contentType: "application/json; charset=utf-8"},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) { wantProblem(t, do(t, h, r), 501, "not_implemented") })
	}
}

func TestBodyLimits(t *testing.T) {
	h := stubRouter(t)
	jsonBody := func(n int) string { return `{"name":"` + strings.Repeat("a", n) + `"}` }
	contentBody := func(n int) string { return `{"body":"` + strings.Repeat("a", n) + `"}` }
	ifMatch := func(v string) map[string]string { return map[string]string{"If-Match": v} }

	t.Run("default JSON limit is 64 KiB", func(t *testing.T) {
		wantProblem(t, do(t, h, req{method: "POST", path: "/api/v1/folders", body: jsonBody(64 << 10), contentType: jsonCT}), 413, "payload_too_large")
		wantProblem(t, do(t, h, req{method: "POST", path: "/api/v1/folders", body: jsonBody(64<<10 - 20), contentType: jsonCT}), 501, "not_implemented")
	})
	t.Run("chunked JSON without Content-Length is limited too", func(t *testing.T) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/folders", io.NopCloser(strings.NewReader(jsonBody(64<<10))))
		r.ContentLength = -1
		r.Header.Set("Content-Type", jsonCT)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		wantProblem(t, rec, 413, "payload_too_large")
	})
	t.Run("content PUT allows 2 MiB", func(t *testing.T) {
		path := "/api/v1/items/" + id1 + "/content"
		wantProblem(t, do(t, h, req{method: "PUT", path: path, body: contentBody(1536 << 10), contentType: jsonCT, headers: ifMatch(`"c-1"`)}), 501, "not_implemented")
		wantProblem(t, do(t, h, req{method: "PUT", path: path, body: contentBody(2 << 20), contentType: jsonCT, headers: ifMatch(`"c-1"`)}), 413, "payload_too_large")
	})
	t.Run("image PUT: multipart up to 10 MiB + 64 KiB, JSON url 64 KiB", func(t *testing.T) {
		path := "/api/v1/items/" + id1 + "/image"
		mp := func(n int) req {
			return req{method: "PUT", path: path, body: strings.Repeat(bigBody, n), contentType: "multipart/form-data; boundary=b", headers: ifMatch(`"i-1"`)}
		}
		wantProblem(t, do(t, h, mp(1<<20)), 501, "not_implemented")
		wantProblem(t, do(t, h, mp(10<<20+64<<10+1)), 413, "payload_too_large")
		big := `{"url":"https://img.example/` + strings.Repeat("a", 64<<10) + `"}`
		wantProblem(t, do(t, h, req{method: "PUT", path: path, body: big, contentType: jsonCT, headers: ifMatch(`"i-1"`)}), 413, "payload_too_large")
	})
	t.Run("missing If-Match is reported before the body is read", func(t *testing.T) {
		wantProblem(t, do(t, h, req{method: "PATCH", path: "/api/v1/items/" + id1, body: "{", contentType: jsonCT}), 428, "precondition_required")
	})
}

// TestCrossCuttingHeaders checks X-Request-Id and the security headers on 2xx,
// 404, 405, 413 and 501 responses. (500 is in the internal tests.)
func TestCrossCuttingHeaders(t *testing.T) {
	h := stubRouter(t)
	tests := []struct {
		name   string
		r      req
		status int
	}{
		{"200 healthz", req{method: "GET", path: "/healthz"}, 200},
		{"200 readyz", req{method: "GET", path: "/readyz"}, 200},
		{"404 unknown path", req{method: "GET", path: "/nope"}, 404},
		{"404 unknown api path", req{method: "GET", path: "/api/v1/nope"}, 404},
		{"405 wrong method", req{method: "POST", path: "/healthz"}, 405},
		{"413 oversize body", req{method: "POST", path: "/api/v1/folders", body: `{"name":"` + strings.Repeat("a", 70<<10) + `"}`, contentType: jsonCT}, 413},
		{"428 precondition", req{method: "DELETE", path: "/api/v1/items/" + id1}, 428},
		{"501 stub", req{method: "GET", path: "/api/v1/tree"}, 501},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.r)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			wantSecurityHeaders(t, rec.Header())
			if tt.status >= 400 {
				if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
					t.Errorf("Content-Type = %q, want application/problem+json", ct)
				}
			}
		})
	}
}

func wantSecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if id := h.Get("X-Request-Id"); id == "" {
		t.Error("missing X-Request-Id")
	}
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header present (D-033: no CORS)")
	}
}

func TestRequestID(t *testing.T) {
	h := stubRouter(t)
	tests := []struct {
		name, in string
		wantSame bool
	}{
		{"safe incoming id is kept", "web-7f3a.2", true},
		{"uuid is kept", id1, true},
		{"spaces are replaced", "has spaces", false},
		{"too long is replaced", strings.Repeat("a", 65), false},
		{"odd characters are replaced", "a;b<script>", false},
		{"empty is generated", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, req{method: "GET", path: "/healthz", headers: map[string]string{"X-Request-Id": tt.in}})
			got := rec.Header().Get("X-Request-Id")
			if tt.wantSame && got != tt.in {
				t.Errorf("id = %q, want %q", got, tt.in)
			}
			if !tt.wantSame && (got == tt.in || len(got) == 0 || len(got) > 64) {
				t.Errorf("id = %q, want a fresh short token", got)
			}
		})
	}
}

func TestHealthBodiesAndContentType(t *testing.T) {
	h := stubRouter(t)
	rec := do(t, h, req{method: "GET", path: "/healthz"})
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "ok" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("healthz = %d %q %q", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	if !bytes.HasSuffix(rec.Body.Bytes(), []byte("\n")) {
		t.Error("healthz body should end with a newline (unchanged from B-00)")
	}
}
