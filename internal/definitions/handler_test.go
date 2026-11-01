package definitions_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// handlerEnv serves the type routes on a migrated database with one workspace.
type handlerEnv struct {
	t    *testing.T
	h    http.Handler
	pool *pgxpool.Pool
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	ws, err := workspace.Bootstrap(t.Context(), pool, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Use(scope.Middleware(ws))
	mgr := definitions.NewService(definitions.NewPostgres(pool), clock.System{})
	definitions.Register(httpserver.NewAPI(r), slog.New(slog.DiscardHandler), mgr)
	return &handlerEnv{t: t, h: r, pool: pool}
}

func (e *handlerEnv) call(method, path, body, ifMatch string) *httptest.ResponseRecorder {
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
	return rec
}

// wantProblemCode asserts a problem+json answer with the status and code, and
// returns the decoded body.
func wantProblemCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if p["code"] != code {
		t.Fatalf("code = %v, want %s; body=%s", p["code"], code, rec.Body)
	}
	return p
}

func TestHandlerTypes_IfMatchRequiredOnTypeWrites(t *testing.T) {
	e := newHandlerEnv(t)
	const path = "/api/v1/types/vehicle"
	tests := []struct {
		name, method, path, body string
	}{
		{"CreateTypeField", http.MethodPost, path + "/fields", `{"label":"Rating","type":"number"}`},
		{"ReorderTypeFields", http.MethodPut, path + "/fields/order", `{"keys":[]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/missing", func(t *testing.T) {
			wantProblemCode(t, e.call(tt.method, tt.path, tt.body, ""), http.StatusPreconditionRequired, "precondition_required")
		})
		t.Run(tt.name+"/field-prefix-is-not-a-type-etag", func(t *testing.T) {
			wantProblemCode(t, e.call(tt.method, tt.path, tt.body, `"kf-1"`), http.StatusPreconditionRequired, "precondition_required")
		})
	}
}

func TestHandlerTypes_StaleVersionAnswers412WithCurrentVersion(t *testing.T) {
	e := newHandlerEnv(t)
	const path = "/api/v1/types/vehicle"

	stale := e.call(http.MethodPost, path+"/fields", `{"label":"Rating","type":"number"}`, `"k-7"`)
	p := wantProblemCode(t, stale, http.StatusPreconditionFailed, "version_conflict")
	if p["current_version"] != float64(1) {
		t.Fatalf("current_version = %v, want 1", p["current_version"])
	}

	// A stale field write names the field's version, not the type's.
	if rec := e.call(http.MethodPost, path+"/fields", `{"label":"Rating","type":"number"}`, `"k-1"`); rec.Code != http.StatusCreated {
		t.Fatalf("create field = %d %s", rec.Code, rec.Body)
	}
	field := e.call(http.MethodPatch, path+"/fields/rating", `{"label":"Score"}`, `"kf-9"`)
	if p := wantProblemCode(t, field, http.StatusPreconditionFailed, "version_conflict"); p["current_version"] != float64(1) {
		t.Fatalf("field current_version = %v, want 1", p["current_version"])
	}
}

func TestHandlerTypes_UnknownBodyKeyIs400(t *testing.T) {
	e := newHandlerEnv(t)
	wantProblemCode(t, e.call(http.MethodPost, "/api/v1/types/vehicle/fields", `{"label":"Rating","type":"number","x":1}`, `"k-1"`), http.StatusBadRequest, "unknown_field")
}

// F022-B1: the custom-type operations are gone, only the list of built-in types stays.
func TestF022B1_CustomTypeOperationsAreGone(t *testing.T) {
	e := newHandlerEnv(t)
	for _, tt := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/types", `{"name":"Board game"}`},
		{http.MethodPatch, "/api/v1/types/vehicle", `{"name":"Cars"}`},
		{http.MethodDelete, "/api/v1/types/vehicle", ""},
	} {
		t.Run(tt.method, func(t *testing.T) {
			rec := e.call(tt.method, tt.path, tt.body, `"k-1"`)
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 404 or 405; body=%s", rec.Code, rec.Body)
			}
		})
	}
}

// F022-B2: the type answer carries no pinned fields, and the built-in types are the five seeded ones.
func TestF022B2_ListTypesHasNoPinnedFields(t *testing.T) {
	e := newHandlerEnv(t)
	rec := e.call(http.MethodGet, "/api/v1/types", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "pinned") {
		t.Fatalf("body mentions pinned fields: %s", rec.Body)
	}
	var types []struct {
		Key      string `json:"key"`
		IsLocked bool   `json:"is_locked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &types); err != nil {
		t.Fatal(err)
	}
	if len(types) != 5 {
		t.Fatalf("types = %+v, want the five built-in types", types)
	}
	for _, typ := range types {
		if !typ.IsLocked {
			t.Errorf("type %s is not locked", typ.Key)
		}
	}
}
