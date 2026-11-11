package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

type settingsBody struct {
	StreamingCountryCode string `json:"streaming_country_code"`
	MetadataLanguage     string `json:"metadata_language"`
	LookupAvailable      bool   `json:"lookup_available"`
	MarkdownHistoryLimit int    `json:"markdown_history_limit"`
	// TrashRetentionDays is nil (JSON null) when off, else the decoded float64.
	TrashRetentionDays any    `json:"trash_retention_days"`
	PreferredStore     string `json:"preferred_store"`
	// OwnedStreamingServices is never null: [] shows every service.
	OwnedStreamingServices []string `json:"owned_streaming_services"`
	Version                int      `json:"version"`
}

// settingsRouter returns a router on a fresh migrated database with the workspace bootstrapped.
func settingsRouter(t *testing.T) http.Handler {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	id, err := workspace.Bootstrap(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return newRouter(t, httpapi.Deps{
		DB: pool, DataDir: t.TempDir(), Logger: quietLogger(),
		WorkspaceID: id, Settings: workspace.NewService(workspace.NewPostgres(pool)), LookupAvailable: true,
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
	want := settingsBody{StreamingCountryCode: "IT", MetadataLanguage: "it-IT", LookupAvailable: true, MarkdownHistoryLimit: 20, PreferredStore: "amazon", OwnedStreamingServices: []string{}, Version: 1}
	if got := decodeSettings(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

func TestAT02_PatchSettings(t *testing.T) {
	h := settingsRouter(t)

	rec := patchSettings(t, h, `"s-1"`, `{"streaming_country_code":"US","metadata_language":"it-IT","markdown_history_limit":0}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("ETag"); got != `"s-2"` {
		t.Errorf("ETag = %q, want \"s-2\"", got)
	}
	want := settingsBody{StreamingCountryCode: "US", MetadataLanguage: "it-IT", LookupAvailable: true, MarkdownHistoryLimit: 0, PreferredStore: "amazon", OwnedStreamingServices: []string{}, Version: 2}
	if got := decodeSettings(t, rec); !reflect.DeepEqual(got, want) {
		t.Errorf("body = %+v, want %+v", got, want)
	}

	// A change-nothing PATCH answers 200 with the current version, no bump.
	rec = patchSettings(t, h, `"s-2"`, `{}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("no-op: status = %d ETag = %q, want 200 \"s-2\"", rec.Code, rec.Header().Get("ETag"))
	}

	get := do(t, h, req{method: "GET", path: "/api/v1/settings"})
	if got := decodeSettings(t, get); !reflect.DeepEqual(got, want) {
		t.Errorf("GET after patches = %+v, want %+v", got, want)
	}
}

// F-027 B5: the workspace name is gone from the settings. A PATCH that sends it is
// the normal unknown-field 400 and changes nothing; GET has no such key.
func TestF027B5_WorkspaceNameGone(t *testing.T) {
	h := settingsRouter(t)
	p := wantProblem(t, patchSettings(t, h, `"s-1"`, `{"workspace_name":"Ours"}`), 400, "unknown_field")
	if p.Detail != `Unknown field "workspace_name".` {
		t.Errorf("detail = %q, want the field name only", p.Detail)
	}
	get := do(t, h, req{method: "GET", path: "/api/v1/settings"})
	if get.Code != 200 || get.Header().Get("ETag") != `"s-1"` {
		t.Errorf("GET after the rejected PATCH: status = %d ETag = %q, want 200 \"s-1\"", get.Code, get.Header().Get("ETag"))
	}
	if strings.Contains(get.Body.String(), "workspace_name") {
		t.Errorf("GET body = %s, want no workspace_name key", get.Body)
	}
}

func TestAT02_PatchSettingsStaleVersion(t *testing.T) {
	h := settingsRouter(t)
	if rec := patchSettings(t, h, `"s-1"`, `{"streaming_country_code":"US"}`); rec.Code != 200 {
		t.Fatalf("setup patch: %d %s", rec.Code, rec.Body)
	}
	p := wantProblem(t, patchSettings(t, h, `"s-1"`, `{"streaming_country_code":"GB"}`), 412, "version_conflict")
	if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
		t.Errorf("current_version = %v, want 2", p.CurrentVersion)
	}
	if got := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"})); got.StreamingCountryCode != "US" {
		t.Errorf("country = %q, want the stale write ignored", got.StreamingCountryCode)
	}
}

func TestAT02_PatchSettingsValidation(t *testing.T) {
	h := settingsRouter(t)
	tests := []struct {
		name, body string
		fields     []string
	}{
		{"bad country", `{"streaming_country_code":"it"}`, []string{"streaming_country_code"}},
		{"bad metadata language", `{"metadata_language":"IT-it"}`, []string{"metadata_language"}},
		{"limit too high", `{"markdown_history_limit":201}`, []string{"markdown_history_limit"}},
		{"several", `{"streaming_country_code":"x","markdown_history_limit":-1}`,
			[]string{"streaming_country_code", "markdown_history_limit"}},
		{"unknown preferred store", `{"preferred_store":"audible"}`, []string{"preferred_store"}},
		{"open library is not a store", `{"preferred_store":"openlibrary"}`, []string{"preferred_store"}},
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
	t.Run("TestF007_default_currency is gone", func(t *testing.T) {
		wantProblem(t, patchSettings(t, h, `"s-1"`, `{"default_currency":"USD"}`), 400, "unknown_field")
	})
	t.Run("missing If-Match", func(t *testing.T) {
		wantProblem(t, patchSettings(t, h, "", `{"streaming_country_code":"US"}`), 428, "precondition_required")
	})
}

func TestF013B5_MetadataLanguageAndLookupAvailability(t *testing.T) {
	h := settingsRouter(t)
	rec := patchSettings(t, h, `"s-1"`, `{"metadata_language":"fr"}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("patch: status=%d ETag=%q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if got := decodeSettings(t, rec); got.MetadataLanguage != "fr" || !got.LookupAvailable || got.Version != 2 {
		t.Fatalf("body = %+v, want fr, available, version 2", got)
	}
	rec = patchSettings(t, h, `"s-2"`, `{"metadata_language":"fr"}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("no-op: status=%d ETag=%q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
}

func TestF012_TrashRetentionSetting(t *testing.T) {
	h := settingsRouter(t)
	if got := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"})); got.TrashRetentionDays != nil {
		t.Fatalf("default retention = %v, want null (off)", got.TrashRetentionDays)
	}

	rec := patchSettings(t, h, `"s-1"`, `{"trash_retention_days":7}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("set 7: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if got := decodeSettings(t, rec); got.TrashRetentionDays != float64(7) || got.Version != 2 {
		t.Fatalf("set 7 body = %+v, want retention 7 version 2", got)
	}

	for _, body := range []string{`{"trash_retention_days":0}`, `{"trash_retention_days":366}`} {
		p := wantProblem(t, patchSettings(t, h, `"s-2"`, body), 422, "validation_failed")
		if len(p.Errors) != 1 || p.Errors[0].Field != "trash_retention_days" {
			t.Errorf("%s: errors = %+v, want field trash_retention_days", body, p.Errors)
		}
	}

	// Repeating the stored value changes nothing: no version bump.
	rec = patchSettings(t, h, `"s-2"`, `{"trash_retention_days":7}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("repeat 7: status = %d ETag = %q, want 200 \"s-2\"", rec.Code, rec.Header().Get("ETag"))
	}

	// An absent field leaves the retention alone.
	rec = patchSettings(t, h, `"s-2"`, `{"markdown_history_limit":5}`)
	if rec.Code != 200 || decodeSettings(t, rec).TrashRetentionDays != float64(7) {
		t.Fatalf("limit only: status = %d body=%s, want retention kept at 7", rec.Code, rec.Body)
	}

	rec = patchSettings(t, h, `"s-3"`, `{"trash_retention_days":null}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-4"` {
		t.Fatalf("set null: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if got := decodeSettings(t, rec); got.TrashRetentionDays != nil || got.Version != 4 {
		t.Fatalf("set null body = %+v, want retention null version 4", got)
	}

	// Null again is a no-op, and the GET shows the null key (not a zero value).
	rec = patchSettings(t, h, `"s-4"`, `{"trash_retention_days":null}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-4"` {
		t.Fatalf("repeat null: status = %d ETag = %q, want 200 \"s-4\"", rec.Code, rec.Header().Get("ETag"))
	}
	get := do(t, h, req{method: "GET", path: "/api/v1/settings"})
	if !strings.Contains(get.Body.String(), `"trash_retention_days":null`) {
		t.Errorf("GET body = %s, want trash_retention_days null", get.Body)
	}
}

// F-016 (0.13.0): the one preferred store is saved, returned, and a repeat is a no-op.
func TestF016_PreferredStoreRoundTrip(t *testing.T) {
	h := settingsRouter(t)
	rec := patchSettings(t, h, `"s-1"`, `{"preferred_store":"kobo"}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("set: status = %d ETag = %q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if got := decodeSettings(t, rec).PreferredStore; got != "kobo" {
		t.Errorf("PATCH answered %q, want kobo", got)
	}
	get := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"}))
	if get.PreferredStore != "kobo" || get.Version != 2 {
		t.Errorf("GET = %+v, want kobo at version 2", get)
	}
	// Repeating the stored value is a no-op: no version bump.
	if rec := patchSettings(t, h, `"s-2"`, `{"preferred_store":"kobo"}`); rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Errorf("same value: status = %d ETag = %q, want 200 \"s-2\"", rec.Code, rec.Header().Get("ETag"))
	}
	// The old list setting is gone: an unknown key is rejected.
	wantProblem(t, patchSettings(t, h, `"s-2"`, `{"preferred_stores":["kobo"]}`), 400, "unknown_field")
}

func TestF016B4_GetStoreProviders(t *testing.T) {
	h := settingsRouter(t)
	rec := do(t, h, req{method: "GET", path: "/api/v1/store-providers"})
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Providers []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"amazon:Amazon", "bookshop:Bookshop.org", "apple_books:Apple Books", "google_play_books:Google Play Books", "kobo:Kobo"}
	var got []string
	for _, p := range body.Providers {
		got = append(got, p.Key+":"+p.Name)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("providers = %v, want %v", got, want)
	}
}

// A wrong JSON type is rejected by the body decoder like on every other settings field.
func TestAT02_PatchSettingsPreferredStoreWrongType(t *testing.T) {
	h := settingsRouter(t)
	wantProblem(t, patchSettings(t, h, `"s-1"`, `{"preferred_store":3}`), 400, "malformed_request")
}

// F-022 B3: the owned streaming services round-trip through GET and PATCH; an
// absent list is unchanged, [] clears it, and a bad key is a 422 on the field.
func TestF022B3_OwnedStreamingServicesHTTP(t *testing.T) {
	h := settingsRouter(t)

	rec := patchSettings(t, h, `"s-1"`, `{"owned_streaming_services":["netflix","primevideo"]}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
		t.Fatalf("set: status = %d ETag = %q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	want := []string{"netflix", "primevideo"}
	if got := decodeSettings(t, rec).OwnedStreamingServices; !reflect.DeepEqual(got, want) {
		t.Errorf("PATCH answered %v, want %v", got, want)
	}
	get := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"}))
	if !reflect.DeepEqual(get.OwnedStreamingServices, want) || get.Version != 2 {
		t.Errorf("GET = %+v, want the list at version 2", get)
	}

	// Absent: the list is kept.
	if rec := patchSettings(t, h, `"s-2"`, `{"markdown_history_limit":5}`); rec.Code != 200 {
		t.Fatalf("limit only: %d %s", rec.Code, rec.Body)
	}
	if got := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"})); !reflect.DeepEqual(got.OwnedStreamingServices, want) {
		t.Errorf("after an unrelated PATCH = %v, want the list kept", got.OwnedStreamingServices)
	}

	// Empty clears it: the answer is [] and not null.
	rec = patchSettings(t, h, `"s-3"`, `{"owned_streaming_services":[]}`)
	if rec.Code != 200 || rec.Header().Get("ETag") != `"s-4"` {
		t.Fatalf("clear: status = %d ETag = %q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"owned_streaming_services":[]`) {
		t.Errorf("body = %s, want owned_streaming_services as []", rec.Body)
	}

	// Bad keys and duplicates are rejected on the field, and nothing changes.
	for _, body := range []string{`{"owned_streaming_services":["Netflix"]}`, `{"owned_streaming_services":["hulu","hulu"]}`} {
		p := wantProblem(t, patchSettings(t, h, `"s-4"`, body), 422, "validation_failed")
		if len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Field, "owned_streaming_services") {
			t.Errorf("errors = %+v, want one on owned_streaming_services", p.Errors)
		}
	}
	if got := decodeSettings(t, do(t, h, req{method: "GET", path: "/api/v1/settings"})); got.Version != 4 || len(got.OwnedStreamingServices) != 0 {
		t.Errorf("after rejected lists = %+v, want version 4 and no list", got)
	}

	// A wrong JSON type is rejected by the body decoder.
	wantProblem(t, patchSettings(t, h, `"s-4"`, `{"owned_streaming_services":"netflix"}`), 400, "malformed_request")
}
