package httpapi_test

import (
	"reflect"
	"testing"
)

func TestF002B5_SummaryCarriesMetadata(t *testing.T) {
	e := newRecentEnv(t)
	movies := e.listOfType("Movies", nil, "movie")
	generic := e.listOfType("Misc", nil, "")
	mid, gid := movies["id"].(string), generic["id"].(string)

	e.newItem(mid, `{"title":"Matrix","metadata":{"release_year":1999,"runtime_minutes":136,"streaming":{"country":"IT","platforms":["Netflix","Prime Video"],"paid_platforms":["Prime Video"]}}}`)
	e.titled(gid, "Plain")

	want := map[string]any{
		"release_year": float64(1999), "runtime_minutes": float64(136),
		"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix", "Prime Video"}, "paid_platforms": []any{"Prime Video"},
			// Computed on read, never stored (spec 0.15.0).
			"platform_links": map[string]any{
				"Netflix":     "https://www.netflix.com/search?q=Matrix",
				"Prime Video": "https://www.primevideo.com/search/?phrase=Matrix",
			}},
	}
	check := func(name string, got any, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s metadata = %v, want %v", name, got, want)
		}
	}

	rows, _ := e.page(mid, "")
	check("list row", rows[0]["metadata"], want)
	grows, _ := e.page(gid, "")
	check("list row (generic)", grows[0]["metadata"], map[string]any{})

	hits := e.search(q("Matrix")).Data
	check("search", hits[0].Item["metadata"], want)
	check("search (generic)", e.search(q("Plain")).Data[0].Item["metadata"], map[string]any{})

	for _, r := range e.recent("").Data {
		switch r.Item["title"] {
		case "Matrix":
			check("recent", r.Item["metadata"], want)
		case "Plain":
			check("recent (generic)", r.Item["metadata"], map[string]any{})
		}
	}
}
