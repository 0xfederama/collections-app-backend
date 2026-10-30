package httpapi_test

import (
	"net/http"
	"testing"
)

// TestF019B3d_ItemQueryEmptyValueIs400 pins the rule the Huma listItems keeps: a
// query parameter that is present but empty is invalid_parameter
// (Huma alone would read it as absent).
func TestF019B3d_ItemQueryEmptyValueIs400(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.list("L", nil)["id"].(string)
	for _, name := range []string{"sort", "q", "filter_field", "limit", "offset"} {
		t.Run(name, func(t *testing.T) {
			p := wantProblem(t, e.call(http.MethodGet, "/api/v1/lists/"+lid+"/items?"+name+"=", "", ""), 400, "invalid_parameter")
			if want := `"` + name + `" is invalid.`; p.Detail != want {
				t.Errorf("detail = %q, want %q", p.Detail, want)
			}
		})
	}
	// An absent parameter is still the default page.
	wantStatus(t, e.call(http.MethodGet, "/api/v1/lists/"+lid+"/items", "", ""), 200)
}
