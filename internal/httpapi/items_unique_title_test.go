package httpapi_test

import (
	"fmt"
	"testing"
)

func TestF026B2_ItemTitlesAreUniquePerList(t *testing.T) {
	e := newTreeEnv(t)
	a := e.list("A", nil)["id"].(string)
	b := e.list("B", nil)["id"].(string)
	amelie := e.titled(a, "Amélie")
	e.titled(b, "Other")

	t.Run("create with an equal title is 409", func(t *testing.T) {
		wantProblem(t, e.call("POST", "/api/v1/lists/"+a+"/items", `{"title":"  amelie "}`, ""), 409, "duplicate_title")
	})
	t.Run("the same title in another list is fine", func(t *testing.T) {
		e.titled(b, "Amélie")
	})
	t.Run("a trashed item does not block", func(t *testing.T) {
		e.trashedItem(a, "Ghost")
		e.titled(a, "ghost")
	})
	t.Run("rename to another item's title is 409", func(t *testing.T) {
		other := e.titled(a, "Other")
		wantProblem(t, e.patchItem(other, `{"title":"AMELIE"}`), 409, "duplicate_title")
	})
	t.Run("rename to a case variant of its own title is 200", func(t *testing.T) {
		wantStatus(t, e.patchItem(amelie, `{"title":"AMÉLIE"}`), 200)
	})
	t.Run("move into a list with an equal title is 409", func(t *testing.T) {
		it := e.titled(b, "Dup")
		e.titled(a, "dup")
		wantProblem(t, e.call("POST", "/api/v1/items/"+it["id"].(string)+"/move", fmt.Sprintf(`{"list_id":%q}`, a), etagOf("i", it)), 409, "duplicate_title")
	})
}
