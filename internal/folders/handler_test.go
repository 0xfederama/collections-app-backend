package folders_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/folders"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

type env struct {
	t    *testing.T
	h    http.Handler
	pool *pgxpool.Pool
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(scope.Middleware(ws))
	folders.Register(httpserver.NewAPI(r), slog.New(slog.DiscardHandler), folders.NewService(folders.NewPostgres(pool), clock.System{}))
	return &env{t: t, h: r, pool: pool}
}

type obj map[string]any

func (e *env) call(method, path, body, ifMatch string) (*httptest.ResponseRecorder, obj) {
	e.t.Helper()
	req := httptest.NewRequestWithContext(e.t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var o obj
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return rec, o
}

func (e *env) folder(name string, parent any) obj {
	e.t.Helper()
	b, _ := json.Marshal(obj{"name": name, "parent_folder_id": parent})
	rec, o := e.call("POST", "/api/v1/folders", string(b), "")
	if rec.Code != 201 {
		e.t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body)
	}
	return o
}

func etagOf(o obj) string { return fmt.Sprintf(`"f-%v"`, o["version"]) }

func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, o obj, status int, code string) {
	t.Helper()
	if rec.Code != status || o["code"] != code {
		t.Fatalf("got %d %v, want %d %s (body %s)", rec.Code, o["code"], status, code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestAT03_FolderHierarchyMoveAndOrder(t *testing.T) {
	e := newEnv(t)
	media := e.folder("Media", nil)
	books := e.folder("Books", nil)
	movies := e.folder("Movies", media["id"])
	if movies["parent_folder_id"] != media["id"] || movies["sort_order"].(float64) != 0 || books["sort_order"].(float64) != 1 || media["parent_folder_id"] != nil {
		t.Fatalf("unexpected folders: %v %v %v", media, books, movies)
	}

	rec, moved := e.call("POST", "/api/v1/folders/"+movies["id"].(string)+"/move", `{"parent_folder_id":null}`, etagOf(movies))
	if rec.Code != 200 || moved["parent_folder_id"] != nil || moved["sort_order"].(float64) != 2 || moved["version"].(float64) != 2 || rec.Header().Get("ETag") != `"f-2"` {
		t.Fatalf("move to root: %d %v etag=%s", rec.Code, moved, rec.Header().Get("ETag"))
	}

	body := fmt.Sprintf(`{"parent_folder_id":null,"ids":[%q,%q,%q]}`, books["id"], movies["id"], media["id"])
	if rec, _ := e.call("PUT", "/api/v1/folders/order", body, ""); rec.Code != 204 {
		t.Fatalf("reorder: %d %s", rec.Code, rec.Body)
	}
	_, got := e.call("GET", "/api/v1/folders/"+media["id"].(string), "", "")
	if got["sort_order"].(float64) != 2 || got["version"].(float64) != 1 {
		t.Fatalf("media after reorder: %v (versions must not bump)", got)
	}
}

func TestAT04_CyclesRejected(t *testing.T) {
	e := newEnv(t)
	a := e.folder("A", nil)
	b := e.folder("B", a["id"])
	c := e.folder("C", b["id"])
	tests := []struct {
		name   string
		folder obj
		dest   obj
	}{{"into itself", a, a}, {"into child", a, b}, {"into grandchild", a, c}, {"middle into its child", b, c}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, o := e.call("POST", "/api/v1/folders/"+tt.folder["id"].(string)+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, tt.dest["id"]), etagOf(tt.folder))
			wantProblem(t, rec, o, 409, "cycle_detected")
		})
	}
	if rec, _ := e.call("POST", "/api/v1/folders/"+c["id"].(string)+"/move", `{"parent_folder_id":null}`, etagOf(c)); rec.Code != 200 {
		t.Fatalf("legal move: %d", rec.Code)
	}
}

func TestAT04_ConcurrentOppositeMovesCannotCreateCycle(t *testing.T) {
	e := newEnv(t)
	a, b := e.folder("A", nil), e.folder("B", nil)
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i, m := range [][2]obj{{a, b}, {b, a}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec, _ := e.call("POST", "/api/v1/folders/"+m[0]["id"].(string)+"/move", fmt.Sprintf(`{"parent_folder_id":%q}`, m[1]["id"]), etagOf(m[0]))
			codes[i] = rec.Code
		}()
	}
	wg.Wait()
	if (codes[0] != 200 || codes[1] != 409) && (codes[0] != 409 || codes[1] != 200) {
		t.Fatalf("status codes = %v, want one 200 and one 409", codes)
	}
}

func TestAT21_FolderReorderValidation(t *testing.T) {
	e := newEnv(t)
	f1, f2 := e.folder("F1", nil), e.folder("F2", nil)
	child := e.folder("Child", f1["id"])
	id := func(o obj) string { return o["id"].(string) }
	cases := map[string][]string{
		"duplicate":     {id(f1), id(f1)},
		"missing":       {id(f1)},
		"unknown":       {id(f1), id(f2), uuid.NewString()},
		"foreign":       {id(f1), id(child)},
		"empty":         {},
		"nothing given": nil,
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			if ids == nil {
				ids = []string{}
			}
			b, _ := json.Marshal(obj{"parent_folder_id": nil, "ids": ids})
			rec, o := e.call("PUT", "/api/v1/folders/order", string(b), "")
			wantProblem(t, rec, o, 409, "order_stale")
		})
	}
	b, _ := json.Marshal(obj{"parent_folder_id": uuid.NewString(), "ids": []string{}})
	rec, o := e.call("PUT", "/api/v1/folders/order", string(b), "")
	wantProblem(t, rec, o, 404, "not_found")
}

func TestVersionAndPreconditions(t *testing.T) {
	e := newEnv(t)
	f := e.folder("F", nil)
	path := "/api/v1/folders/" + f["id"].(string)

	rec, got := e.call("PATCH", path, `{"name":"F2"}`, etagOf(f))
	if rec.Code != 200 || got["name"] != "F2" || got["version"].(float64) != 2 {
		t.Fatalf("rename: %d %v", rec.Code, got)
	}
	rec, o := e.call("PATCH", path, `{"name":"X"}`, `"f-1"`)
	wantProblem(t, rec, o, 412, "version_conflict")
	if o["current_version"].(float64) != 2 {
		t.Fatalf("current_version = %v", o["current_version"])
	}
	for _, h := range []string{"", "*", `"i-2"`, `W/"f-2"`} {
		rec, o := e.call("PATCH", path, `{"name":"X"}`, h)
		wantProblem(t, rec, o, 428, "precondition_required")
	}
	// A PATCH that changes nothing answers 200 and does not bump the version.
	rec, got = e.call("PATCH", path, `{}`, `"f-2"`)
	if rec.Code != 200 || got["version"].(float64) != 2 {
		t.Fatalf("empty patch: %d %v", rec.Code, got)
	}
}

func TestRequestValidation(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name, body string
		status     int
		code       string
	}{
		{"blank name", `{"name":"   "}`, 422, "validation_failed"},
		{"name too long", `{"name":"` + strings.Repeat("x", 201) + `"}`, 422, "validation_failed"},
		{"missing name", `{}`, 422, "validation_failed"},
		{"unknown field", `{"name":"A","color":"red"}`, 400, "unknown_field"},
		{"not json", `{"name":`, 400, "malformed_request"},
		{"missing destination", `{"name":"A","parent_folder_id":"` + uuid.NewString() + `"}`, 409, "destination_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, o := e.call("POST", "/api/v1/folders", tt.body, "")
			wantProblem(t, rec, o, tt.status, tt.code)
		})
	}
	rec, o := e.call("POST", "/api/v1/folders", `{"name":"`+strings.Repeat("x", 70<<10)+`"}`, "")
	wantProblem(t, rec, o, 413, "payload_too_large")
	rec, o = e.call("GET", "/api/v1/folders/not-a-uuid", "", "")
	wantProblem(t, rec, o, 400, "invalid_parameter")
	rec, o = e.call("GET", "/api/v1/folders/"+uuid.NewString(), "", "")
	wantProblem(t, rec, o, 404, "not_found")
}
