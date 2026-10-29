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
	mgr := definitions.NewManager(definitions.NewPostgres(pool), clock.System{})
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
	created := e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game"}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	const path = "/api/v1/types/board_game"
	tests := []struct {
		name, method, path, body string
	}{
		{"UpdateType", http.MethodPatch, path, `{"name":"Games"}`},
		{"DeleteType", http.MethodDelete, path, ""},
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
	wantStatus := func(rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("status = %d, want %d; body=%s", rec.Code, status, rec.Body)
		}
	}
	wantStatus(e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game"}`, ""), http.StatusCreated)
	const path = "/api/v1/types/board_game"

	stale := e.call(http.MethodPatch, path, `{"name":"Games"}`, `"k-7"`)
	p := wantProblemCode(t, stale, http.StatusPreconditionFailed, "version_conflict")
	if p["current_version"] != float64(1) {
		t.Fatalf("current_version = %v, want 1", p["current_version"])
	}
	staleDelete := e.call(http.MethodDelete, path, "", `"k-0"`)
	if p := wantProblemCode(t, staleDelete, http.StatusPreconditionFailed, "version_conflict"); p["current_version"] != float64(1) {
		t.Fatalf("delete current_version = %v, want 1", p["current_version"])
	}

	// A stale field write names the field's version, not the type's.
	wantStatus(e.call(http.MethodPost, path+"/fields", `{"label":"Rating","type":"number"}`, `"k-1"`), http.StatusCreated)
	field := e.call(http.MethodPatch, path+"/fields/rating", `{"label":"Score"}`, `"kf-9"`)
	if p := wantProblemCode(t, field, http.StatusPreconditionFailed, "version_conflict"); p["current_version"] != float64(1) {
		t.Fatalf("field current_version = %v, want 1", p["current_version"])
	}
}

func TestHandlerTypes_UnknownBodyKeyIs400(t *testing.T) {
	e := newHandlerEnv(t)
	wantProblemCode(t, e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game","colour":"red"}`, ""), http.StatusBadRequest, "unknown_field")
	created := e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game"}`, "")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	wantProblemCode(t, e.call(http.MethodPatch, "/api/v1/types/board_game", `{"colour":"red"}`, `"k-1"`), http.StatusBadRequest, "unknown_field")
	wantProblemCode(t, e.call(http.MethodPost, "/api/v1/types/board_game/fields", `{"label":"Rating","type":"number","x":1}`, `"k-1"`), http.StatusBadRequest, "unknown_field")
}

func TestHandlerTypes_NewTypeHasEmptyFieldsArray(t *testing.T) {
	e := newHandlerEnv(t)
	rec := e.call(http.MethodPost, "/api/v1/types", `{"name":"Board game","default_pinned_fields":null}`, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"fields":[]`) || !strings.Contains(rec.Body.String(), `"default_pinned_fields":null`) {
		t.Fatalf("create body = %s", rec.Body)
	}
	if rec.Header().Get("Location") != "/api/v1/types/board_game" || rec.Header().Get("ETag") != `"k-1"` {
		t.Fatalf("Location = %q, ETag = %q", rec.Header().Get("Location"), rec.Header().Get("ETag"))
	}
}
