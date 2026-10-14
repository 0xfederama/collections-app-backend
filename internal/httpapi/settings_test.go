package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

type settingsBody struct {
	WorkspaceName        string `json:"workspace_name"`
	StreamingCountryCode string `json:"streaming_country_code"`
	DefaultCurrency      string `json:"default_currency"`
	MarkdownHistoryLimit int    `json:"markdown_history_limit"`
	Version              int    `json:"version"`
}

// settingsRouter returns a router on a fresh migrated database with the workspace bootstrapped.
func settingsRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	id, err := workspace.Bootstrap(t.Context(), pool, "Our Collection")
	if err != nil {
		t.Fatal(err)
	}
	return newRouter(t, httpapi.Deps{
		DB: pool, DataDir: t.TempDir(), Logger: quietLogger(),
		WorkspaceID: id, Settings: workspace.NewService(pool),
	})
}

func decodeSettings(t *testing.T, rec *httptest.ResponseRecorder) settingsBody {
	t.Helper()
	var s settingsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return s
}

func patchSettings(t *testing.T, h http.Handler, ifMatch, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := req{method: "PATCH", path: "/api/v1/settings", body: body, contentType: jsonCT}
	if ifMatch != "" {
		r.headers = map[string]string{"If-Match": ifMatch}
	}
	return do(t, h, r)
}

func TestAT02_GetSettings(t *testing.T) {
	h := settingsRouter(t)
	rec := do(t, h, req{method: "GET", path: "/api/v1/settings"})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != `"s-1"` {
		t.Errorf("ETag = %q, want \"s-1\"", got)
	}
	want := settingsBody{"Our Collection", "IT", "EUR", 20, 1}
	if got := decodeSettings(t, rec); got != want {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

func TestAT02_PatchSettings(t *testing.T) {
	h := settingsRouter(t)

	rec := patchSettings(t, h, `"s-1"`, `{"workspace_name":" Home ","streaming_country_code":"US","default_currency":"USD","markdown_history_limit":0}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != `"s-2"` {
		t.Errorf("ETag = %q, want \"s-2\"", got)
	}
	want := settingsBody{"Home", "US", "USD", 0, 2}
	if got := decodeSettings(t, rec); got != want {
		t.Errorf("body = %+v, want %+v", got, want)
	}

	// The name alone also bumps the one settings ETag stream.
	rec = patchSettings(t, h, `"s-2"`, `{"workspace_name":"Ours"}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-3"` {
		t.Fatalf("rename: status = %d ETag = %q, want 200 \"s-3\"", rec.Code, rec.Header().Get("ETag"))
	}

	// A change-nothing PATCH answers 200 with the current version, no bump.
	rec = patchSettings(t, h, `"s-3"`, `{}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-3"` {
		t.Fatalf("no-op: status = %d ETag = %q, want 200 \"s-3\"", rec.Code, rec.Header().Get("ETag"))
	}

	get := do(t, h, req{method: "GET", path: "/api/v1/settings"})
	if got, want := decodeSettings(t, get), (settingsBody{"Ours", "US", "USD", 0, 3}); got != want {
		t.Errorf("GET after patches = %+v, want %+v", got, want)
	}
}

func TestAT02_PatchSettingsStaleVersion(t *testing.T) {
	h := settingsRouter(t)
	if rec := patchSettings(t, h, `"s-1"`, `{"default_currency":"USD"}`); rec.Code != 200 {
		t.Fatalf("setup patch: %d %s", rec.Code, rec.Body)
	}
	p := wantProblem(t, patchSettings(t, h, `"s-1"`, `{"default_currency":"GBP"}`), 412, "version_conflict")
	if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
		t.Errorf("current_version = %v, want 2", p.CurrentVersion)
	}
	if got := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"})); got.DefaultCurrency != "USD" {
		t.Errorf("currency = %q, want the stale write ignored", got.DefaultCurrency)
	}
}

func TestAT02_PatchSettingsValidation(t *testing.T) {
	h := settingsRouter(t)
	tests := []struct {
		name, body string
		fields     []string
	}{
		{"empty name", `{"workspace_name":"  "}`, []string{"workspace_name"}},
		{"bad country", `{"streaming_country_code":"it"}`, []string{"streaming_country_code"}},
		{"bad currency", `{"default_currency":"EURO"}`, []string{"default_currency"}},
		{"limit too high", `{"markdown_history_limit":201}`, []string{"markdown_history_limit"}},
		{"several", `{"workspace_name":"","default_currency":"x","markdown_history_limit":-1}`,
			[]string{"workspace_name", "default_currency", "markdown_history_limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := wantProblem(t, patchSettings(t, h, `"s-1"`, tt.body), 422, "validation_failed")
			if len(p.Errors) != len(tt.fields) {
				t.Fatalf("errors = %+v, want fields %v", p.Errors, tt.fields)
			}
			for i, f := range tt.fields {
				if p.Errors[i].Field != f || p.Errors[i].Message == "" {
					t.Errorf("errors[%d] = %+v, want field %q with a message", i, p.Errors[i], f)
				}
			}
		})
	}
	t.Run("unknown field is a 400, not a silent ignore", func(t *testing.T) {
		wantProblem(t, patchSettings(t, h, `"s-1"`, `{"version":9}`), 400, "unknown_field")
	})
	t.Run("missing If-Match", func(t *testing.T) {
		wantProblem(t, patchSettings(t, h, "", `{"default_currency":"USD"}`), 428, "precondition_required")
	})
}
