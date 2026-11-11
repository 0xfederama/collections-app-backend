package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/0xfederama/collections-app-backend/internal/httpapi"
	"github.com/0xfederama/collections-app-backend/internal/integrations/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/testdb"
	"github.com/0xfederama/collections-app-backend/internal/search"
	"github.com/0xfederama/collections-app-backend/internal/storage"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// The refresh mode of POST /items/{id}/lookup and POST /lists/{id}/lookup-refresh
// (D-123), over HTTP. TMDB is a local fake whose movie data the test changes; the
// poster is a PNG from the same fake, so no test reaches the network.

// tmdbData is what the fake TMDB serves: titles and runtimes by id, and whether it is down.
type tmdbData struct {
	mu       sync.Mutex
	titles   map[int]string
	runtimes map[int]int
	down     bool
}

func (d *tmdbData) set(id int, title string, runtime int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.titles[id], d.runtimes[id] = title, runtime
}

func (d *tmdbData) setDown(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = down
}

func newTMDBFake(t *testing.T, d *tmdbData) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case d.down:
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/w500/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(coverPNG())
		case r.URL.Path == "/3/search/movie":
			_, _ = w.Write([]byte(`{"results":[]}`))
		case strings.HasPrefix(r.URL.Path, "/3/movie/"):
			id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/3/movie/"))
			title, ok := d.titles[id]
			if err != nil || !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%d,"title":%q,"original_title":%q,"release_date":"1999-03-31","runtime":%d,"poster_path":"/p.jpg"}`,
				id, title, title, d.runtimes[id])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type refreshEnv struct {
	*treeEnv
	tmdb *tmdbData
}

// newRefreshEnv serves movies from a fake TMDB; tmdbOff leaves the server without a TMDB key.
func newRefreshEnv(t *testing.T, tmdbOff bool) *refreshEnv {
	t.Helper()
	pool := testdb.NewMigrated(t, sharedPostgres(t))
	ws, err := workspace.Bootstrap(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	data := &tmdbData{titles: map[int]string{603: "The Matrix", 604: "Matrix Reloaded"}, runtimes: map[int]int{603: 136, 604: 107}}
	upstream := newTMDBFake(t, data)
	client := tmdb.NewWithOptions("test-key", tmdb.Options{APIBaseURL: upstream.URL + "/3", ImageBaseURL: upstream.URL, HTTPClient: upstream.Client()})
	if tmdbOff {
		client = tmdb.New("")
	}
	movies := lookup.NewTMDBProvider(client)

	dataDir := t.TempDir()
	files, err := storage.NewLocal(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	itemSvc := newItems(pool, clock.System{})
	settingsSvc := workspace.NewService(workspace.NewPostgres(pool))
	store := lookup.NewStore(pool, files, clock.System{}, itemSvc, quietLogger(), ws)
	svc := lookup.New(lookup.Providers{types.Movie: movies, types.TVShow: movies}, store, itemSvc, settingsSvc, clock.System{}, ws)
	h := newRouter(t, httpapi.Deps{
		DB: pool, DataDir: dataDir, Logger: quietLogger(), WorkspaceID: ws, Settings: settingsSvc,
		Items: itemSvc, Search: search.NewService(search.NewPostgres(pool)),
		TMDB: movies, LookupAvailable: client.Enabled(), Lookup: svc,
	})
	return &refreshEnv{treeEnv: &treeEnv{t: t, h: h, pool: pool, ws: ws}, tmdb: data}
}

// refreshItem is the lookup call of the item with the given body (refresh, here).
func (e *refreshEnv) refreshItem(it obj, body string) *httptest.ResponseRecorder {
	return e.call(http.MethodPost, "/api/v1/items/"+it["id"].(string)+"/lookup", body, fmt.Sprintf(`"i-%v"`, it["version"]))
}

func TestRefreshMode_OverHTTPOverwritesTheItemFromTMDB(t *testing.T) {
	e := newRefreshEnv(t, false)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	it := e.newItem(lid, `{"title":"Matrix","tmdb_id":603}`)
	if it["title"] != "The Matrix" {
		t.Fatalf("created item = %v", it)
	}
	e.tmdb.set(603, "The Matrix Remastered", 138)

	stale := obj{"id": it["id"], "version": float64(9)}
	wantProblem(t, e.refreshItem(stale, `{"mode":"refresh"}`), http.StatusPreconditionFailed, "version_conflict")

	rec := e.refreshItem(it, `{"mode":"refresh"}`)
	wantStatus(t, rec, http.StatusOK)
	got := decode(t, rec)
	if got["title"] != "The Matrix Remastered" || got["version"] != float64(2) || rec.Header().Get("ETag") != `"i-2"` {
		t.Fatalf("after refresh = %v, ETag %q", got, rec.Header().Get("ETag"))
	}
	if m := metaOf(got); m["runtime_minutes"] != float64(138) || m["tmdb_id"] != float64(603) {
		t.Errorf("metadata = %v", m)
	}
}

func TestRefreshMode_OverHTTPFailureChangesNothing(t *testing.T) {
	e := newRefreshEnv(t, false)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	it := e.newItem(lid, `{"title":"Matrix","tmdb_id":603}`)
	e.tmdb.setDown(true)
	wantProblem(t, e.refreshItem(it, `{"mode":"refresh"}`), http.StatusServiceUnavailable, "upstream_unavailable")
	after := decode(t, e.call(http.MethodGet, "/api/v1/items/"+it["id"].(string), "", ""))
	if after["title"] != "The Matrix" || after["version"] != float64(1) {
		t.Fatalf("a failed refresh changed the item: %v", after)
	}
}

func TestRefreshList_OverHTTPCountsEveryItem(t *testing.T) {
	e := newRefreshEnv(t, false)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	matrix := e.newItem(lid, `{"title":"Matrix","tmdb_id":603}`)
	reloaded := e.newItem(lid, `{"title":"Reloaded","tmdb_id":604}`)
	plain := e.newItem(lid, `{"title":"Unknown film"}`)
	off := e.newItem(lid, `{"title":"Off","lookup":false}`)
	e.tmdb.set(603, "The Matrix Remastered", 138)
	e.tmdb.set(604, "Matrix Reloaded Again", 110)

	// No If-Match: the call is per item versioned internally.
	rec := e.call(http.MethodPost, "/api/v1/lists/"+lid+"/lookup-refresh", "", "")
	wantStatus(t, rec, http.StatusOK)
	if got := decode(t, rec); got["updated"] != float64(2) || got["skipped"] != float64(2) || got["failed"] != float64(0) || got["remaining"] != float64(0) {
		t.Fatalf("counts = %v", got)
	}
	if e.itemTitle(matrix) != "The Matrix Remastered" || e.itemTitle(reloaded) != "Matrix Reloaded Again" {
		t.Errorf("titles = %q, %q", e.itemTitle(matrix), e.itemTitle(reloaded))
	}
	if e.itemTitle(plain) != "Unknown film" || e.itemTitle(off) != "Off" {
		t.Errorf("skipped items changed: %q, %q", e.itemTitle(plain), e.itemTitle(off))
	}
}

func TestRefreshList_OverHTTPOutageAndBooksAndMissingLists(t *testing.T) {
	e := newRefreshEnv(t, false)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	e.newItem(lid, `{"title":"Matrix","tmdb_id":603}`)
	e.newItem(lid, `{"title":"Reloaded","tmdb_id":604}`)

	e.tmdb.setDown(true)
	if got := decode(t, e.call(http.MethodPost, "/api/v1/lists/"+lid+"/lookup-refresh", "", "")); got["updated"] != float64(0) || got["failed"] != float64(2) {
		t.Fatalf("an outage must stop the run and count the items as failed: %v", got)
	}
	e.tmdb.setDown(false)

	books := e.listOfType("Books", nil, "book")["id"].(string)
	e.newItem(books, `{"title":"Dune","lookup":false}`)
	if got := decode(t, e.call(http.MethodPost, "/api/v1/lists/"+books+"/lookup-refresh", "", "")); got["updated"] != float64(0) || got["skipped"] != float64(1) {
		t.Fatalf("a book list is skipped: %v", got)
	}

	wantProblem(t, e.call(http.MethodPost, "/api/v1/lists/"+"00000000-0000-4000-8000-000000000000"+"/lookup-refresh", "", ""), http.StatusNotFound, "not_found")
}

func TestRefreshList_OverHTTPTMDBOffIs503(t *testing.T) {
	e := newRefreshEnv(t, true)
	lid := e.listOfType("Films", nil, "movie")["id"].(string)
	wantProblem(t, e.call(http.MethodPost, "/api/v1/lists/"+lid+"/lookup-refresh", "", ""), http.StatusServiceUnavailable, "lookup_disabled")
}

// itemTitle reads an item's title over HTTP.
func (e *refreshEnv) itemTitle(it obj) string {
	return decode(e.t, e.call(http.MethodGet, "/api/v1/items/"+it["id"].(string), "", ""))["title"].(string)
}
