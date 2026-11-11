package httpapi_test

import (
	"strings"
	"testing"
)

// TestB3a_SettingsServedByHuma checks that the settings operations, now served
// by Huma on the router, keep the request rules: unknown keys,
// Content-Type, If-Match, body limit and the trash retention null/absent cases.
func TestB3a_SettingsServedByHuma(t *testing.T) {
	t.Run("GET and PATCH answer through the router", func(t *testing.T) {
		h := settingsRouter(t)
		if rec := do(t, h, req{method: "GET", path: "/api/v1/settings"}); rec.Code != 200 || rec.Header().Get("ETag") != `"s-1"` {
			t.Fatalf("GET: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
		}
		if rec := patchSettings(t, h, `"s-1"`, `{"markdown_history_limit":5}`); rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
			t.Fatalf("PATCH: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
		}
	})

	t.Run("undeclared key is 400 unknown_field", func(t *testing.T) {
		h := settingsRouter(t)
		p := wantProblem(t, patchSettings(t, h, `"s-1"`, `{"markdown_history_limit":5,"version":9}`), 400, "unknown_field")
		if p.Detail != `Unknown field "version".` {
			t.Errorf("detail = %q, want the field name only", p.Detail)
		}
	})

	t.Run("wrong or missing Content-Type is 400 malformed_request and never echoes the body", func(t *testing.T) {
		h := settingsRouter(t)
		const body = `{"preferred_store":"Echoed"}`
		tests := []struct {
			name, contentType string
		}{
			{"text/plain", "text/plain"},
			{"missing", ""},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := do(t, h, req{method: "PATCH", path: "/api/v1/settings", body: body, contentType: tt.contentType,
					headers: map[string]string{"If-Match": `"s-1"`}})
				p := wantProblem(t, rec, 400, "malformed_request")
				if p.Detail != "Unsupported Content-Type." {
					t.Errorf("detail = %q, want Unsupported Content-Type.", p.Detail)
				}
				if strings.Contains(rec.Body.String(), "Echoed") {
					t.Errorf("the request body was echoed: %s", rec.Body)
				}
			})
		}
	})

	t.Run("stale If-Match is 412 with the current version", func(t *testing.T) {
		h := settingsRouter(t)
		if rec := patchSettings(t, h, `"s-1"`, `{"markdown_history_limit":5}`); rec.Code != 200 {
			t.Fatalf("setup patch: %d %s", rec.Code, rec.Body)
		}
		p := wantProblem(t, patchSettings(t, h, `"s-1"`, `{"markdown_history_limit":6}`), 412, "version_conflict")
		if p.CurrentVersion == nil || *p.CurrentVersion != 2 {
			t.Errorf("current_version = %v, want 2", p.CurrentVersion)
		}
	})

	t.Run("missing If-Match is 428", func(t *testing.T) {
		h := settingsRouter(t)
		wantProblem(t, patchSettings(t, h, "", `{"markdown_history_limit":5}`), 428, "precondition_required")
	})

	t.Run("body over 64 KiB is 413 payload_too_large", func(t *testing.T) {
		h := settingsRouter(t)
		body := `{"preferred_store":"` + strings.Repeat("a", 70<<10) + `"}`
		wantProblem(t, patchSettings(t, h, `"s-1"`, body), 413, "payload_too_large")
	})

	t.Run("trash_retention_days: null turns it off, absent keeps it", func(t *testing.T) {
		h := settingsRouter(t)
		if rec := patchSettings(t, h, `"s-1"`, `{"trash_retention_days":7}`); rec.Code != 200 || rec.Header().Get("ETag") != `"s-2"` {
			t.Fatalf("set 7: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
		}

		// Absent: the retention stays, the limit change bumps the version.
		rec := patchSettings(t, h, `"s-2"`, `{"markdown_history_limit":5}`)
		if rec.Code != 200 || decodeSettings(t, rec).TrashRetentionDays != float64(7) {
			t.Fatalf("absent: status = %d body=%s, want retention kept at 7", rec.Code, rec.Body)
		}

		// Null: the retention goes off and the response carries the key as null.
		rec = patchSettings(t, h, `"s-3"`, `{"trash_retention_days":null}`)
		if rec.Code != 200 || rec.Header().Get("ETag") != `"s-4"` {
			t.Fatalf("null: status = %d ETag = %q, body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body)
		}
		if !strings.Contains(rec.Body.String(), `"trash_retention_days":null`) {
			t.Errorf("body = %s, want trash_retention_days null", rec.Body)
		}
		if got := decodeSettings(t, rec); got.TrashRetentionDays != nil || got.Version != 4 {
			t.Errorf("null body = %+v, want retention null version 4", got)
		}

		// Absent again, after null: still off.
		if rec := patchSettings(t, h, `"s-4"`, `{"markdown_history_limit":5}`); rec.Code != 200 || decodeSettings(t, rec).TrashRetentionDays != nil {
			t.Errorf("absent after null: status = %d body=%s, want retention still null", rec.Code, rec.Body)
		}
	})
}
