package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/logs"
)

// TestLogRoutesOnlyWhenBufferGiven: with Deps.Logs nil (LOGS_UI off) the log routes
// answer the normal 404 problem; with a buffer they serve JSON and the viewer page.
func TestLogRoutesOnlyWhenBufferGiven(t *testing.T) {
	base := httpapi.Deps{DB: fakePinger{}, DataDir: t.TempDir(), Logger: quietLogger()}

	off := newRouter(t, base)
	for _, path := range []string{"/admin/logs", "/admin/logs/view"} {
		code, body := get(t, off, http.MethodGet, path)
		if code != http.StatusNotFound || !strings.Contains(body, `"not_found"`) {
			t.Errorf("disabled %s: status %d, body %s; want 404 not_found", path, code, body)
		}
	}

	base.Logs = logs.NewBuffer(10)
	on := newRouter(t, base)
	if code, body := get(t, on, http.MethodGet, "/admin/logs"); code != http.StatusOK || !strings.Contains(body, `"total"`) {
		t.Errorf("enabled /admin/logs: status %d, body %s", code, body)
	}
	if code, _ := get(t, on, http.MethodGet, "/admin/logs/view"); code != http.StatusOK {
		t.Errorf("enabled /admin/logs/view: status %d", code)
	}
}

// TestLogRoutesStayOutOfTheSpec: enabling the buffer does not change the public contract.
func TestLogRoutesStayOutOfTheSpec(t *testing.T) {
	api := httpapi.API(httpapi.Deps{Logs: logs.NewBuffer(10)})
	for path := range api.OpenAPI().Paths {
		if strings.HasPrefix(path, "/admin") {
			t.Errorf("%s is in the public spec", path)
		}
	}
}
