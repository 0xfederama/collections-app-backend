package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// The refresh mode of POST /items/{id}/lookup and the list refresh (refreshListLookups).
// They run the real DBStore over the in-memory repository, so the overwrite, the
// poster and the rollback rules are the ones the weekly pass uses.

var refreshNow = time.Date(2026, time.November, 4, 10, 0, 0, 0, time.UTC)

// tmdbFake is a TMDB provider whose answers are scripted per id. An id with no
// scripted answer gets fresh details when defaults is set, else a 404.
type tmdbFake struct {
	details   map[string]Details
	errs      map[string]error
	defaults  bool
	disabled  bool
	calls     []string
	downloads int
	// onFetch runs before a details fetch answers (a concurrent edit in the middle of a run).
	onFetch func(id string)
}

func (f *tmdbFake) Name() string                     { return ProviderTMDB }
func (f *tmdbFake) Enabled() bool                    { return !f.disabled }
func (f *tmdbFake) ValidateSourceID(id string) error { return ValidSourceID(ProviderTMDB, id) }
func (f *tmdbFake) Search(context.Context, string, string, string) ([]Suggestion, error) {
	return nil, nil
}

func (f *tmdbFake) Details(_ context.Context, _, id, _, _ string) (Details, error) {
	f.calls = append(f.calls, id)
	if f.onFetch != nil {
		f.onFetch(id)
	}
	if err := f.errs[id]; err != nil {
		return Details{}, err
	}
	if d, ok := f.details[id]; ok {
		return d, nil
	}
	if f.defaults {
		return Details{SourceID: id, Title: "Fresh " + id, Fields: map[string]any{"release_year": 2000}}, nil
	}
	return Details{}, ErrMatchNotFound
}

func (f *tmdbFake) DownloadImage(context.Context, string) ([]byte, error) {
	f.downloads++
	return nil, errors.New("no poster in these tests")
}

// fullItems reads items from the fake with their type, metadata and image, as the
// item service does (trashed items are not found).
type fullItems struct{ repo *fakeRepo }

func (g fullItems) Get(_ context.Context, id uuid.UUID) (items.Item, error) {
	it, ok := g.repo.items[id]
	if !ok || it.deleted {
		return items.Item{}, apperr.NotFound("item")
	}
	out := items.Item{ID: id, ListID: it.listID, Title: it.title, Version: it.version, Type: it.typ, Metadata: it.metadata}
	if it.imageURL != nil {
		out.Image = &items.Image{Source: items.ImageSourceURL, URL: *it.imageURL}
	}
	return out, nil
}

func (fullItems) Create(context.Context, uuid.UUID, items.CreateInput) (items.Item, error) {
	return items.Item{}, errors.New("not used by the refresh")
}

func (fullItems) Update(context.Context, uuid.UUID, int, items.Patch) (items.Item, error) {
	return items.Item{}, errors.New("not used by the refresh")
}

// refreshService wires the service over the env's DBStore, with TMDB as the movie and TV provider.
func refreshService(e *applyEnv, tmdb Provider) *Service {
	return New(Providers{"movie": tmdb, "tv_show": tmdb}, e.store, fullItems{e.repo},
		fakeSettings{workspace.Settings{MetadataLanguage: "it-IT", StreamingCountryCode: "IT"}},
		testClock{now: refreshNow}, uuid.New())
}

// matrixDetails is what TMDB answers for id 603 now.
func matrixDetails() Details {
	return Details{SourceID: "603", Title: "The Matrix", Fields: map[string]any{"release_year": 1999, "runtime_minutes": 136}, ImagePath: ""}
}

// importedFields records a TMDB import whose fetched fields are fields.
func importedFields(e *applyEnv, id uuid.UUID, sourceID string, fields map[string]any, fetchedAt time.Time) {
	raw, _ := json.Marshal(fields)
	e.repo.imports[id] = importSave{
		ItemID: id, Provider: ProviderTMDB, SourceID: sourceID, Type: "movie", ImportedFields: raw,
		OriginalTitle: "Matrix", FetchedAt: fetchedAt,
	}
}

func metadataOf(t *testing.T, e *applyEnv, id uuid.UUID) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(e.repo.items[id].metadata, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ptrInt(v int) *int { return &v }

// ---- refresh mode of POST /items/{id}/lookup ----

func TestRefreshMode_OverwritesFetchedFieldsAndTitleKeepsUserImage(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	id := e.addItem("My Matrix", map[string]any{"tmdb_id": 603, "runtime_minutes": 140, "release_year": 1999, "imdb_url": "https://imdb.example/old", "director": "Mine"})
	importedFields(e, id, "603", map[string]any{"runtime_minutes": 140, "release_year": 1999, "imdb_url": "https://imdb.example/old"}, refreshNow.AddDate(0, 0, -2))
	e.repo.edit(id, func(it *fakeItem) { it.imageURL = ptr("https://user.example/x.jpg") })
	tmdb := &tmdbFake{details: map[string]Details{"603": matrixDetails()}}

	if _, err := refreshService(e, tmdb).Apply(t.Context(), id, 1, ModeRefresh, Selection{}); err != nil {
		t.Fatal(err)
	}
	it := e.repo.items[id]
	if it.title != "The Matrix" || it.version != 2 {
		t.Fatalf("title = %q version %d, want the TMDB title at version 2", it.title, it.version)
	}
	m := metadataOf(t, e, id)
	if m["runtime_minutes"] != float64(136) || m["release_year"] != float64(1999) {
		t.Errorf("fetched fields = %v, want the TMDB values", m)
	}
	if _, ok := m["imdb_url"]; ok {
		t.Errorf("imdb_url is gone from TMDB and must be removed: %v", m)
	}
	if m["director"] != "Mine" {
		t.Errorf("a user-only field must stay: %v", m)
	}
	if it.imageURL == nil || *it.imageURL != "https://user.example/x.jpg" || tmdb.downloads != 0 {
		t.Errorf("user image changed (downloads %d)", tmdb.downloads)
	}
	if tmdb.calls[0] != "603" {
		t.Errorf("details fetched for %v, want the stored tmdb_id", tmdb.calls)
	}
}

func TestRefreshMode_BookAndUnmatchedItemsAreValidationErrors(t *testing.T) {
	t.Run("a book answers 422 without a call", func(t *testing.T) {
		e := newApplyEnv(t, refreshNow)
		id := e.addItem("Dune", map[string]any{"openlibrary_id": "OL1W"})
		e.repo.edit(id, func(it *fakeItem) { it.typ = ptr("book") })
		ol := &fakeClient{name: ProviderOpenLibrary, enabled: true, details: Details{SourceID: "OL1W"}}
		svc := New(Providers{"book": ol}, e.store, fullItems{e.repo}, fakeSettings{}, testClock{now: refreshNow}, uuid.New())
		_, err := svc.Apply(t.Context(), id, 1, ModeRefresh, Selection{})
		var v *apperr.ValidationError
		if !errors.As(err, &v) || v.Errors[0].Field != "mode" || ol.detailsID != "" || e.repo.commits != 0 {
			t.Fatalf("err = %v, details %q, commits %d", err, ol.detailsID, e.repo.commits)
		}
	})
	t.Run("no TMDB id and none selected", func(t *testing.T) {
		e := newApplyEnv(t, refreshNow)
		id := e.addItem("Plain", map[string]any{"release_year": 1999})
		tmdb := &tmdbFake{}
		_, err := refreshService(e, tmdb).Apply(t.Context(), id, 1, ModeRefresh, Selection{})
		var v *apperr.ValidationError
		if !errors.As(err, &v) || v.Errors[0].Field != "tmdb_id" || len(tmdb.calls) != 0 {
			t.Fatalf("err = %v, calls %v", err, tmdb.calls)
		}
	})
	t.Run("a malformed selected id", func(t *testing.T) {
		e := newApplyEnv(t, refreshNow)
		id := e.addItem("Matrix", map[string]any{"tmdb_id": 603})
		tmdb := &tmdbFake{}
		_, err := refreshService(e, tmdb).Apply(t.Context(), id, 1, ModeRefresh, Selection{TMDBID: ptrInt(0)})
		var v *apperr.ValidationError
		if !errors.As(err, &v) || len(tmdb.calls) != 0 {
			t.Fatalf("err = %v, calls %v", err, tmdb.calls)
		}
	})
}

func TestRefreshMode_SelectedIdReplacesTheStoredMatch(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	id := e.addItem("Matrix", map[string]any{"tmdb_id": 603})
	tmdb := &tmdbFake{details: map[string]Details{"604": {SourceID: "604", Title: "Matrix Reloaded", Fields: map[string]any{"release_year": 2003}}}}
	if _, err := refreshService(e, tmdb).Apply(t.Context(), id, 1, ModeRefresh, Selection{TMDBID: ptrInt(604)}); err != nil {
		t.Fatal(err)
	}
	if e.repo.items[id].title != "Matrix Reloaded" || metadataOf(t, e, id)["tmdb_id"] != float64(604) {
		t.Fatalf("item = %+v", e.repo.items[id])
	}
}

func TestRefreshMode_FailuresChangeNothingAndMapToUpstreamCodes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		disable bool
		check   func(error) bool
	}{
		{name: "TMDB rate limit", err: ErrProviderRateLimited, check: isUpstream},
		{name: "TMDB 404 for the id", err: ErrMatchNotFound, check: isUpstream},
		{name: "TMDB unreachable", err: errors.New("TMDB request failed"), check: isUpstream},
		{name: "TMDB key rejected", err: ErrDisabled, check: isLookupDisabled},
		{name: "server without TMDB", disable: true, check: isLookupDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newApplyEnv(t, refreshNow)
			id := e.addItem("Matrix", map[string]any{"tmdb_id": 603})
			tmdb := &tmdbFake{errs: map[string]error{"603": tc.err}, disabled: tc.disable}
			_, err := refreshService(e, tmdb).Apply(t.Context(), id, 1, ModeRefresh, Selection{})
			if !tc.check(err) || e.repo.items[id].title != "Matrix" || e.repo.commits != 0 {
				t.Fatalf("err = %v, title %q, commits %d", err, e.repo.items[id].title, e.repo.commits)
			}
		})
	}
}

func TestRefreshMode_IgnoresLookupDisabledAndChecksTheVersion(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	id := e.addItem("Matrix", map[string]any{"tmdb_id": 603, "lookup_disabled": true})
	tmdb := &tmdbFake{details: map[string]Details{"603": matrixDetails()}}
	svc := refreshService(e, tmdb)
	if _, err := svc.Apply(t.Context(), id, 0, ModeRefresh, Selection{}); !isVersionConflict(err) || len(tmdb.calls) != 0 {
		t.Fatalf("a stale version: err = %v, calls %v", err, tmdb.calls)
	}
	if _, err := svc.Apply(t.Context(), id, 1, ModeRefresh, Selection{}); err != nil {
		t.Fatal(err)
	}
	if e.repo.items[id].title != "The Matrix" {
		t.Fatalf("an explicit refresh of a lookup_disabled item must run, title %q", e.repo.items[id].title)
	}
}

// ---- list refresh ----

func TestRefreshList_CountsUpdatesAndSkips(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	older := refreshNow.AddDate(0, 0, -3)
	a := addTMDB(e, "Matrix", map[string]any{"tmdb_id": 603}, ptrTime(refreshNow.AddDate(0, 0, -2)))
	importedFields(e, a, "603", map[string]any{"runtime_minutes": 140}, refreshNow.AddDate(0, 0, -2))
	e.repo.edit(a, func(it *fakeItem) { it.imageURL = ptr("https://user.example/x.jpg") })
	addTMDB(e, "No id", map[string]any{"release_year": 1999}, nil)
	addTMDB(e, "Lookup off", map[string]any{"tmdb_id": 605, "lookup_disabled": true}, nil)
	d := addTMDB(e, "Older", map[string]any{"tmdb_id": 606}, &older)
	trashed := addTMDB(e, "Trashed", map[string]any{"tmdb_id": 607}, nil)
	e.repo.edit(trashed, func(it *fakeItem) { it.deleted = true })
	addTMDB(e, "String id", map[string]any{"tmdb_id": "608"}, nil)
	tmdb := &tmdbFake{details: map[string]Details{"603": matrixDetails(), "606": {SourceID: "606", Title: "Older title"}}}

	got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ListRefreshResult{Updated: 2, Skipped: 3}) {
		t.Fatalf("result = %+v, want 2 updated and 4 skipped", got)
	}
	// The oldest fetch goes first.
	if len(tmdb.calls) != 2 || tmdb.calls[0] != "606" || tmdb.calls[1] != "603" {
		t.Errorf("calls = %v, want the older fetch first", tmdb.calls)
	}
	if e.repo.items[d].title != "Older title" {
		t.Errorf("older item title = %q", e.repo.items[d].title)
	}
	if e.repo.items[a].title != "The Matrix" || e.repo.items[a].imageURL == nil || *e.repo.items[a].imageURL != "https://user.example/x.jpg" {
		t.Errorf("the refreshed item lost its user image: %+v", e.repo.items[a])
	}
}

func TestRefreshList_BooksAndUntypedListsAreAllSkipped(t *testing.T) {
	for _, typ := range []*string{ptr("book"), nil} {
		e := newApplyEnv(t, refreshNow)
		e.repo.lists[e.list] = typ
		addTMDB(e, "One", map[string]any{"tmdb_id": 603}, nil)
		addTMDB(e, "Two", map[string]any{"openlibrary_id": "OL1W"}, nil)
		tmdb := &tmdbFake{defaults: true}
		got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
		if err != nil || got != (ListRefreshResult{Skipped: 2}) || len(tmdb.calls) != 0 {
			t.Fatalf("type %v: result %+v err %v calls %v", typ, got, err, tmdb.calls)
		}
	}
}

// ageAll gives the movies fetch times 3, 2 and 1 days ago in that order: the first is refreshed first.
func ageAll(e *applyEnv, ids ...uuid.UUID) {
	for i, id := range ids {
		importedFields(e, id, fmt.Sprint(600+i), map[string]any{}, refreshNow.AddDate(0, 0, -(3-i)))
	}
}

func TestRefreshList_RateLimitStopsTheRunAndCountsTheRest(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	x := addTMDB(e, "X", map[string]any{"tmdb_id": 600}, nil)
	y := addTMDB(e, "Y", map[string]any{"tmdb_id": 601}, nil)
	z := addTMDB(e, "Z", map[string]any{"tmdb_id": 602}, nil)
	ageAll(e, x, y, z)
	tmdb := &tmdbFake{
		details: map[string]Details{"600": {SourceID: "600", Title: "X new"}},
		errs:    map[string]error{"601": ErrProviderRateLimited},
	}
	got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ListRefreshResult{Updated: 1, Failed: 2}) {
		t.Fatalf("result = %+v, want 1 updated and the rate-limited item and the one after it failed", got)
	}
	if len(tmdb.calls) != 2 || e.repo.items[z].title != "Z" || e.repo.items[y].title != "Y" {
		t.Errorf("calls %v; the items after the rate limit must not be touched", tmdb.calls)
	}
}

func TestRefreshList_OutageStopsButAMissingMatchDoesNot(t *testing.T) {
	t.Run("an outage stops the run", func(t *testing.T) {
		e := newApplyEnv(t, refreshNow)
		x := addTMDB(e, "X", map[string]any{"tmdb_id": 600}, nil)
		y := addTMDB(e, "Y", map[string]any{"tmdb_id": 601}, nil)
		ageAll(e, x, y)
		tmdb := &tmdbFake{errs: map[string]error{"600": errors.New("TMDB request failed (HTTP 500)")}}
		got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
		if err != nil || got != (ListRefreshResult{Failed: 2}) || len(tmdb.calls) != 1 {
			t.Fatalf("result %+v err %v calls %v", got, err, tmdb.calls)
		}
	})
	t.Run("a 404 for one item goes on with the next", func(t *testing.T) {
		e := newApplyEnv(t, refreshNow)
		x := addTMDB(e, "X", map[string]any{"tmdb_id": 600}, nil)
		y := addTMDB(e, "Y", map[string]any{"tmdb_id": 601}, nil)
		ageAll(e, x, y)
		tmdb := &tmdbFake{errs: map[string]error{"600": ErrMatchNotFound}, details: map[string]Details{"601": {SourceID: "601", Title: "Y new"}}}
		got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
		if err != nil || got != (ListRefreshResult{Updated: 1, Failed: 1}) || len(tmdb.calls) != 2 {
			t.Fatalf("result %+v err %v calls %v", got, err, tmdb.calls)
		}
		if e.repo.items[x].title != "X" || e.repo.items[y].title != "Y new" {
			t.Errorf("titles %q %q: the failed item keeps its values", e.repo.items[x].title, e.repo.items[y].title)
		}
	})
}

func TestRefreshList_ItemChangedDuringTheRunIsSkipped(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	x := addTMDB(e, "X", map[string]any{"tmdb_id": 600}, nil)
	y := addTMDB(e, "Y", map[string]any{"tmdb_id": 601}, nil)
	ageAll(e, x, y)
	tmdb := &tmdbFake{
		defaults: true,
		onFetch: func(id string) {
			if id == "600" {
				// The user edits Y while X is being fetched: Y's version moves on.
				e.repo.edit(y, func(it *fakeItem) { it.version++ })
			}
		},
	}
	got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
	if err != nil || got != (ListRefreshResult{Updated: 1, Skipped: 1}) {
		t.Fatalf("result %+v err %v", got, err)
	}
	if e.repo.items[y].title != "Y" {
		t.Errorf("the edited item must not be overwritten, title %q", e.repo.items[y].title)
	}
}

func TestRefreshList_OneCallIsBoundedAndTakesTheOldestFirst(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	const total = listRefreshBatchSize + 5
	for i := range total {
		// Item i was fetched i+1 hours ago: the largest i is the oldest.
		id := addTMDB(e, "Movie", map[string]any{"tmdb_id": 1000 + i}, nil)
		importedFields(e, id, fmt.Sprint(1000+i), map[string]any{}, refreshNow.Add(-time.Duration(i+1)*time.Hour))
	}
	tmdb := &tmdbFake{defaults: true}
	got, err := refreshService(e, tmdb).RefreshList(t.Context(), e.list)
	if err != nil {
		t.Fatal(err)
	}
	if got != (ListRefreshResult{Updated: listRefreshBatchSize, Skipped: 5}) || len(tmdb.calls) != listRefreshBatchSize {
		t.Fatalf("result %+v, %d calls", got, len(tmdb.calls))
	}
	if tmdb.calls[0] != fmt.Sprint(1000+total-1) {
		t.Errorf("first call %s, want the oldest fetch", tmdb.calls[0])
	}
	for _, id := range tmdb.calls {
		if id == "1000" || id == "1001" {
			t.Fatalf("the most recent fetches must wait for the next call: %v", tmdb.calls)
		}
	}
}

func TestRefreshList_UnknownListAndTMDBOffAreErrors(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	svc := refreshService(e, &tmdbFake{})
	var missing *apperr.NotFoundError
	if _, err := svc.RefreshList(t.Context(), uuid.New()); !errors.As(err, &missing) {
		t.Fatalf("unknown list: err = %v", err)
	}
	addTMDB(e, "Matrix", map[string]any{"tmdb_id": 603}, nil)
	off := &tmdbFake{disabled: true}
	if _, err := refreshService(e, off).RefreshList(t.Context(), e.list); !isLookupDisabled(err) || len(off.calls) != 0 {
		t.Fatalf("TMDB off: err = %v", err)
	}
}

func TestRefreshList_CancelledRequestStopsBeforeAnyFetch(t *testing.T) {
	e := newApplyEnv(t, refreshNow)
	addTMDB(e, "Matrix", map[string]any{"tmdb_id": 603}, nil)
	tmdb := &tmdbFake{defaults: true}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := refreshService(e, tmdb).RefreshList(ctx, e.list); !errors.Is(err, context.Canceled) || len(tmdb.calls) != 0 {
		t.Fatalf("err = %v, calls %v", err, tmdb.calls)
	}
}

// ---- helpers ----

func ptrTime(t time.Time) *time.Time { return &t }

func isUpstream(err error) bool {
	var u *apperr.UpstreamUnavailableError
	return errors.As(err, &u)
}

func isLookupDisabled(err error) bool {
	var d *apperr.LookupDisabledError
	return errors.As(err, &d)
}

func isVersionConflict(err error) bool {
	var c *apperr.VersionConflictError
	return errors.As(err, &c)
}
