package httpapi_test

import (
	"testing"
)

// TestF019B3d_ContentRequiredFields pins the spec's required body fields on the Huma
// content operations: a save without body or a restore without version is a 422
// and creates no version (the old guard let an empty object save an empty body).
// Restore of version 0 keeps its 404: the spec's minimum is not enforced on the body.
func TestF019B3d_ContentRequiredFields(t *testing.T) {
	e := newContentEnv(t)
	_, url := e.contentItem()
	e.save(url, 0, "kept")

	wantProblem(t, e.call("PUT", url, `{}`, `"c-1"`), 422, "validation_failed")
	wantProblem(t, e.call("POST", url+"/restore", `{}`, `"c-1"`), 422, "validation_failed")
	wantProblem(t, e.call("POST", url+"/restore", `{"version":0}`, `"c-1"`), 404, "not_found")

	if got := e.versions(url); len(got) != 1 {
		t.Fatalf("rejected requests created versions: %v", got)
	}
}
