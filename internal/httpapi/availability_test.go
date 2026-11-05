package httpapi_test

import "testing"

// F026-B1: the availability filters read owned_streaming_services and combine with q.
func TestF026B1_ListItemsAvailability(t *testing.T) {
	e := newTreeEnv(t)
	lid := e.listOfType("Movies", nil, "movie")["id"].(string)
	e.newItem(lid, `{"title":"Heat","metadata":{"streaming":{"country":"IT","platforms":["Netflix"]}}}`)
	e.newItem(lid, `{"title":"Alien","metadata":{"streaming":{"country":"IT","platforms":["Google Play"],"paid_platforms":["Google Play"]}}}`)
	e.newItem(lid, `{"title":"Ran"}`)
	titles := func(query string) []string {
		rows, _ := e.page(lid, "?"+query)
		out := []string{}
		for _, r := range rows {
			out = append(out, r["title"].(string))
		}
		return out
	}
	check := func(query string, want ...string) {
		t.Helper()
		got := titles(query)
		if len(got) != len(want) {
			t.Fatalf("%s: got %v, want %v", query, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: got %v, want %v", query, got, want)
			}
		}
	}

	// No settings row: every service counts.
	check("availability=available&sort=title", "Alien", "Heat")
	check("availability=included", "Heat")
	check("availability=available&q=ali", "Alien")

	e.exec(`INSERT INTO workspace_settings (workspace_id, owned_streaming_services) VALUES ($1, '["googleplay"]')`, e.ws)
	check("availability=available", "Alien")
	check("availability=included")

	wantProblem(t, e.call("GET", "/api/v1/lists/"+lid+"/items?availability=free", "", ""), 400, "invalid_parameter")
	books := e.listOfType("Books", nil, "book")["id"].(string)
	wantProblem(t, e.call("GET", "/api/v1/lists/"+books+"/items?availability=available", "", ""), 400, "invalid_parameter")
}
