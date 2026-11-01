package httpapi_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
)

// newContentEnv is a treeEnv whose router also serves content, with a settings
// row (markdown_history_limit defaults to 20).
func newContentEnv(t *testing.T) *treeEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", ws); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspace_settings (workspace_id) VALUES ($1)", ws); err != nil {
		t.Fatal(err)
	}
	h := newRouter(t, httpapi.Deps{
		DB: pool, DataDir: t.TempDir(), Logger: quietLogger(), WorkspaceID: ws,
		Items: newItems(pool, clock.System{}),
	})
	return &treeEnv{t: t, h: h, pool: pool, ws: ws}
}

// contentItem creates a list with one item and returns the item's content URL.
func (e *treeEnv) contentItem() (itemID, url string) {
	e.t.Helper()
	l := e.list("L", nil)
	it := e.titled(l["id"].(string), "Item")
	id := it["id"].(string)
	return id, "/api/v1/items/" + id + "/content"
}

// save PUTs body with the given content version and returns the decoded Content.
func (e *treeEnv) save(url string, ifMatch int, body string) obj {
	e.t.Helper()
	b, _ := json.Marshal(obj{"body": body})
	rec := e.call("PUT", url, string(b), fmt.Sprintf(`"c-%d"`, ifMatch))
	wantStatus(e.t, rec, 200)
	c := decode(e.t, rec)
	if got, want := rec.Header().Get("ETag"), fmt.Sprintf(`"c-%v"`, c["version"]); got != want {
		e.t.Fatalf("ETag = %s, want %s", got, want)
	}
	return c
}

func sameInstant(t *testing.T, a, b any) bool {
	t.Helper()
	ta, errA := time.Parse(time.RFC3339Nano, fmt.Sprint(a))
	tb, errB := time.Parse(time.RFC3339Nano, fmt.Sprint(b))
	if errA != nil || errB != nil {
		t.Fatalf("bad timestamps %v, %v", a, b)
	}
	return ta.Equal(tb)
}

func (e *treeEnv) versions(url string) []int {
	e.t.Helper()
	rec := e.call("GET", url+"/versions", "", "")
	wantStatus(e.t, rec, 200)
	var body struct {
		Data []struct {
			Version   int `json:"version"`
			SizeBytes int `json:"size_bytes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		e.t.Fatal(err)
	}
	out := make([]int, 0, len(body.Data))
	for _, v := range body.Data {
		out = append(out, v.Version)
	}
	return out
}

func TestAT08_ContentGetEmptyAndSaved(t *testing.T) {
	e := newContentEnv(t)
	id, url := e.contentItem()

	rec := e.call("GET", url, "", "")
	wantStatus(t, rec, 200)
	c := decode(t, rec)
	if c["version"] != float64(0) || c["body"] != "" || c["updated_at"] != nil || c["item_id"] != id {
		t.Fatalf("empty content = %v", c)
	}
	if rec.Header().Get("ETag") != `"c-0"` {
		t.Fatalf("ETag = %s", rec.Header().Get("ETag"))
	}

	saved := e.save(url, 0, "# Notes")
	if saved["version"] != float64(1) || saved["body"] != "# Notes" || saved["updated_at"] == nil {
		t.Fatalf("saved = %v", saved)
	}
	got := decode(t, e.call("GET", url, "", ""))
	if got["body"] != "# Notes" || got["version"] != float64(1) {
		t.Fatalf("get = %v", got)
	}
}

func TestAT08_SaveRules(t *testing.T) {
	e := newContentEnv(t)
	_, url := e.contentItem()
	e.save(url, 0, "one")

	tests := []struct {
		name    string
		ifMatch string
		body    string
		status  int
		code    string
	}{
		{"stale version", `"c-0"`, `{"body":"two"}`, 412, "version_conflict"},
		{"ahead version", `"c-5"`, `{"body":"two"}`, 412, "version_conflict"},
		{"over 1 MiB", `"c-1"`, `{"body":"` + strings.Repeat("a", content.MaxBodyBytes+1) + `"}`, 413, "payload_too_large"},
		// 400k three-byte runes: under 1 Mi characters but 1.2 MB of UTF-8.
		{"over 1 MiB in bytes, under in characters", `"c-1"`, `{"body":"` + strings.Repeat("€", 400_000) + `"}`, 413, "payload_too_large"},
		{"NUL character", `"c-1"`, `{"body":"a\u0000b"}`, 422, "validation_failed"},
		{"unknown field", `"c-1"`, `{"body":"x","extra":1}`, 400, "unknown_field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.call("PUT", url, tc.body, tc.ifMatch)
			wantStatus(t, rec, tc.status)
			if p := decode(t, rec); p["code"] != tc.code {
				t.Fatalf("problem = %v", p)
			}
			if tc.status == 412 && decode(t, rec)["current_version"] != float64(1) {
				t.Fatalf("current_version missing: %s", rec.Body)
			}
		})
	}
	if got := e.versions(url); len(got) != 1 {
		t.Fatalf("rejected saves created versions: %v", got)
	}

	exactly := strings.Repeat("a", content.MaxBodyBytes)
	if c := e.save(url, 1, exactly); c["version"] != float64(2) {
		t.Fatalf("exactly 1 MiB must be accepted: %v", c["version"])
	}
}

// The route accepts 2 MiB of JSON, so escaped text that is 1.5 MiB on the wire
// but short once decoded is saved, while the 1 MiB rule counts decoded bytes.
func TestAT08_JSONBodyLimitIsSeparateFromTextLimit(t *testing.T) {
	e := newContentEnv(t)
	_, url := e.contentItem()
	wire := `{"body":"` + strings.Repeat(`\u0061`, 256<<10) + `"}` // 1.5 MiB of JSON, 256 KiB of text
	rec := e.call("PUT", url, wire, `"c-0"`)
	wantStatus(t, rec, 200)
	if c := decode(t, rec); len(c["body"].(string)) != 256<<10 {
		t.Fatalf("body length = %d", len(c["body"].(string)))
	}
}

func TestAT08_IdenticalSaveCreatesNoVersion(t *testing.T) {
	e := newContentEnv(t)
	id, url := e.contentItem()
	first := e.save(url, 0, "same")
	e.exec("UPDATE items SET updated_at = now() - interval '1 hour' WHERE id = $1", id)
	var before time.Time
	if err := e.pool.QueryRow(t.Context(), "SELECT updated_at FROM items WHERE id = $1", id).Scan(&before); err != nil {
		t.Fatal(err)
	}

	again := e.save(url, 1, "same")
	if again["version"] != float64(1) || !sameInstant(t, again["updated_at"], first["updated_at"]) {
		t.Fatalf("identical save = %v, first = %v", again, first)
	}
	var after time.Time
	if err := e.pool.QueryRow(t.Context(), "SELECT updated_at FROM items WHERE id = $1", id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("identical save bumped updated_at: %v -> %v", before, after)
	}
	if got := e.versions(url); len(got) != 1 {
		t.Fatalf("versions = %v", got)
	}
	// Saving the empty body of version 0 is not a save either.
	_, url2 := e.contentItem()
	if c := e.save(url2, 0, ""); c["version"] != float64(0) || c["updated_at"] != nil {
		t.Fatalf("empty save of version 0 = %v", c)
	}
}

func TestAT08_SaveBumpsUpdatedAtNotItemVersion(t *testing.T) {
	e := newContentEnv(t)
	id, url := e.contentItem()
	e.exec("UPDATE items SET updated_at = now() - interval '1 hour' WHERE id = $1", id)
	before := e.fetchItem(id)

	e.save(url, 0, "hello")
	after := e.fetchItem(id)
	if after["version"] != before["version"] {
		t.Fatalf("items.version changed: %v -> %v", before["version"], after["version"])
	}
	if after["updated_at"] == before["updated_at"] {
		t.Fatalf("updated_at not bumped: %v", after["updated_at"])
	}
	if after["content_version"] != float64(1) {
		t.Fatalf("content_version = %v", after["content_version"])
	}
}

func TestAT08_RetentionKeepsLatestPlusLimit(t *testing.T) {
	tests := []struct {
		limit int
		saves int
		want  []int
	}{
		{limit: 2, saves: 5, want: []int{5, 4, 3}},
		{limit: 0, saves: 3, want: []int{3}},
		{limit: 20, saves: 3, want: []int{3, 2, 1}},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("limit=%d", tc.limit), func(t *testing.T) {
			e := newContentEnv(t)
			e.exec("UPDATE workspace_settings SET markdown_history_limit = $1", tc.limit)
			_, url := e.contentItem()
			for i := range tc.saves {
				e.save(url, i, fmt.Sprintf("body %d", i+1))
			}
			if got := e.versions(url); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("versions = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("lowering the limit prunes on the next save", func(t *testing.T) {
		e := newContentEnv(t)
		_, url := e.contentItem()
		for i := range 5 {
			e.save(url, i, fmt.Sprintf("body %d", i+1))
		}
		e.exec("UPDATE workspace_settings SET markdown_history_limit = 1")
		if got := e.versions(url); len(got) != 5 {
			t.Fatalf("pruned too early: %v", got)
		}
		e.save(url, 5, "body 6")
		if got := e.versions(url); fmt.Sprint(got) != "[6 5]" {
			t.Fatalf("versions = %v", got)
		}
	})
}

func TestAT08_VersionsAndPreview(t *testing.T) {
	e := newContentEnv(t)
	_, url := e.contentItem()
	e.save(url, 0, "a")
	e.save(url, 1, "bb")
	e.save(url, 2, "ccc")

	rec := e.call("GET", url+"/versions", "", "")
	var list struct {
		Data []obj `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 3 || list.Data[0]["version"] != float64(3) || list.Data[0]["size_bytes"] != float64(3) || list.Data[2]["version"] != float64(1) {
		t.Fatalf("list = %s", rec.Body)
	}

	rec = e.call("GET", url+"/versions/2", "", "")
	wantStatus(t, rec, 200)
	if c := decode(t, rec); c["body"] != "bb" || c["version"] != float64(2) || rec.Header().Get("ETag") != `"c-2"` {
		t.Fatalf("version 2 = %s, ETag %s", rec.Body, rec.Header().Get("ETag"))
	}
	for _, path := range []string{"/versions/4", "/versions/0"} {
		if rec := e.call("GET", url+path, "", ""); rec.Code != 404 && rec.Code != 400 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
	}
	wantStatus(t, e.call("GET", url+"/versions/4", "", ""), 404)
}

func TestAT08_Restore(t *testing.T) {
	e := newContentEnv(t)
	e.exec("UPDATE workspace_settings SET markdown_history_limit = 1")
	_, url := e.contentItem()
	e.save(url, 0, "first")
	e.save(url, 1, "second")

	restore := func(ifMatch string, version int) (int, obj) {
		rec := e.call("POST", url+"/restore", fmt.Sprintf(`{"version":%d}`, version), ifMatch)
		return rec.Code, decode(t, rec)
	}

	if status, p := restore(`"c-1"`, 1); status != 412 || p["current_version"] != float64(2) {
		t.Fatalf("stale restore = %d %v", status, p)
	}
	status, c := restore(`"c-2"`, 1)
	if status != 200 || c["version"] != float64(3) || c["body"] != "first" {
		t.Fatalf("restore = %d %v", status, c)
	}
	// limit = 1 keeps versions 3 and 2: version 1 was pruned by that restore.
	if got := e.versions(url); fmt.Sprint(got) != "[3 2]" {
		t.Fatalf("versions = %v", got)
	}
	if status, p := restore(`"c-3"`, 1); status != 404 || p["code"] != "not_found" {
		t.Fatalf("pruned restore = %d %v", status, p)
	}
	// Restoring a body equal to the current one is a no-op.
	if status, c := restore(`"c-3"`, 3); status != 200 || c["version"] != float64(3) {
		t.Fatalf("restore of current = %d %v", status, c)
	}
	if got := e.versions(url); fmt.Sprint(got) != "[3 2]" {
		t.Fatalf("versions after no-op = %v", got)
	}
}

func TestAT08_TrashedAndMissingItem(t *testing.T) {
	e := newContentEnv(t)
	id, url := e.contentItem()
	e.save(url, 0, "x")
	batch := e.trashBatch("item", uuid.MustParse(id))
	e.exec("UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1", id, batch)
	missing := "/api/v1/items/" + uuid.NewString() + "/content"

	for _, u := range []string{url, missing} {
		for _, tc := range []struct{ method, path, body, ifMatch string }{
			{"GET", u, "", ""},
			{"PUT", u, `{"body":"y"}`, `"c-1"`},
			{"GET", u + "/versions", "", ""},
			{"GET", u + "/versions/1", "", ""},
			{"POST", u + "/restore", `{"version":1}`, `"c-1"`},
		} {
			if rec := e.call(tc.method, tc.path, tc.body, tc.ifMatch); rec.Code != 404 {
				t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
			}
		}
	}
}

func TestAT08_ConcurrentSavesOneWins(t *testing.T) {
	e := newContentEnv(t)
	_, url := e.contentItem()
	e.save(url, 0, "base")

	const n = 8
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			rec := e.call("PUT", url, fmt.Sprintf(`{"body":"writer %d"}`, i), `"c-1"`)
			codes[i] = rec.Code
		})
	}
	wg.Wait()

	wins, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case 200:
			wins++
		case 412:
			conflicts++
		default:
			t.Fatalf("unexpected status %d in %v", c, codes)
		}
	}
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d (%v)", wins, conflicts, codes)
	}
	if got := e.versions(url); fmt.Sprint(got) != "[2 1]" {
		t.Fatalf("versions = %v", got)
	}
}
