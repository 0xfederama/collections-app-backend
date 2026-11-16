package lookup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// F-028 B4 (D-164): the generic backfill pass, on the fake repository. The Postgres selection is in
// backfill_db_test.go.

// generationRefresh is a refresh that succeeds and writes the current import generation, as Apply does.
func generationRefresh(e *applyEnv, calls *[]uuid.UUID) RefreshFunc {
	return func(_ context.Context, id uuid.UUID, _ int) error {
		*calls = append(*calls, id)
		row := e.repo.imports[id]
		row.FetchedAt = e.clock.Now()
		row.ImportGeneration = currentImportGeneration
		e.repo.imports[id] = row
		return nil
	}
}

// addBelowGeneration puts a TMDB movie whose import an earlier generation wrote, fetched at at.
func addBelowGeneration(e *applyEnv, title string, at time.Time) uuid.UUID {
	id := addTMDB(e, title, map[string]any{"tmdb_id": 603}, &at)
	return id
}

func TestB4_BatchIsCappedAndResumesFromTheOldestFetch(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	var ids []uuid.UUID
	for i := 0; i < 30; i++ {
		// Item i was fetched i hours ago: the oldest fetches (the highest i) go first.
		ids = append(ids, addBelowGeneration(e, "Movie", now.Add(-time.Duration(i)*time.Hour)))
	}
	var calls []uuid.UUID
	refresh := generationRefresh(e, &calls)

	first, err := e.store.Backfill(t.Context(), refresh, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if first.Refreshed != backfillBatchSize || first.Remaining != 5 || first.Complete {
		t.Fatalf("first pass = %+v, want 25 refreshed, 5 remaining, not complete", first)
	}
	if calls[0] != ids[29] {
		t.Errorf("first call = %v, want the oldest fetch", calls[0])
	}

	second, err := e.store.Backfill(t.Context(), refresh, refresh)
	if err != nil {
		t.Fatal(err)
	}
	if second.Refreshed != 5 || second.Remaining != 0 || !second.Complete {
		t.Fatalf("second pass = %+v, want 5 refreshed, none remaining, complete", second)
	}
	if len(calls) != 30 {
		t.Fatalf("calls = %d, want every item once", len(calls))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range calls {
		if seen[c] {
			t.Fatalf("item %v re-read", c)
		}
		seen[c] = true
	}
}

// TestB4_SecondPassIsANoOpAndWritesNoContent: after the backfill, a pass calls nothing, and each
// item's description was filled once (the lookup writes the summary through Apply).
func TestB4_SecondPassIsANoOpAndWritesNoContent(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		ids = append(ids, addBelowGeneration(e, "Movie", now.Add(-time.Duration(i+1)*time.Hour)))
	}
	applyRefresh := func(ctx context.Context, id uuid.UUID, version int) error {
		_, err := e.store.Apply(ctx, ApplyParams{
			Mode: ModeScheduled, ItemID: id, ExpectedVersion: version, Type: "movie",
			Provider: ProviderTMDB, SourceID: "603", Title: "The Matrix", OriginalTitle: "matrix",
			Fields: map[string]any{"release_year": 1999}, Synopsis: "A summary.", HistoryLimit: 20,
		})
		return err
	}
	if res, err := e.store.Backfill(t.Context(), applyRefresh, applyRefresh); err != nil || res.Refreshed != 3 || !res.Complete {
		t.Fatalf("pass = %+v err %v, want three re-reads and complete", res, err)
	}
	for _, id := range ids {
		if n := len(e.repo.content[id]); n != 1 {
			t.Fatalf("versions = %d, want the summary once", n)
		}
	}
	var calls []uuid.UUID
	res, err := e.store.Backfill(t.Context(), generationRefresh(e, &calls), generationRefresh(e, &calls))
	if err != nil || res.Refreshed != 0 || res.Remaining != 0 || res.Complete || len(calls) != 0 {
		t.Errorf("second pass = %+v err %v calls %d, want nothing to do and no second completion", res, err, len(calls))
	}
	for _, id := range ids {
		if n := len(e.repo.content[id]); n != 1 {
			t.Errorf("second pass wrote content: %d versions", n)
		}
	}
}

// Trashed and lookup_disabled items stay below the generation and are not counted: the pass completes
// without them.
func TestB4_SkipsTrashedAndLookupDisabledItems(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	normal := addBelowGeneration(e, "Normal", now.Add(-time.Hour))
	trashed := addBelowGeneration(e, "Trashed", now.Add(-2*time.Hour))
	e.repo.edit(trashed, func(it *fakeItem) { it.deleted = true })
	disabled := e.addItem("Off", map[string]any{"tmdb_id": 604, "lookup_disabled": true})
	e.repo.imports[disabled] = importSave{ItemID: disabled, Provider: ProviderTMDB, SourceID: "604", Type: "movie", ImportedFields: []byte("{}"), FetchedAt: now.Add(-3 * time.Hour)}

	var calls []uuid.UUID
	res, err := e.store.Backfill(t.Context(), generationRefresh(e, &calls), generationRefresh(e, &calls))
	if err != nil || res.Refreshed != 1 || res.Remaining != 0 || !res.Complete {
		t.Fatalf("pass = %+v err %v, want the one normal item and complete", res, err)
	}
	if len(calls) != 1 || calls[0] != normal {
		t.Errorf("calls = %v, want only the normal item", calls)
	}
	if e.repo.imports[trashed].ImportGeneration != 0 || e.repo.imports[disabled].ImportGeneration != 0 {
		t.Error("a skipped item was written: it must stay below the generation")
	}
}

// A rate limit stops the pass at once; the items not reached stay below the generation.
func TestB4_StopsOnRateLimit(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	for i := 0; i < 5; i++ {
		addBelowGeneration(e, "Movie", now.Add(-time.Duration(i+1)*time.Hour))
	}
	var calls []uuid.UUID
	ok := generationRefresh(e, &calls)
	limited := func(ctx context.Context, id uuid.UUID, v int) error {
		if len(calls) == 1 {
			calls = append(calls, id)
			return ErrProviderRateLimited
		}
		return ok(ctx, id, v)
	}
	res, err := e.store.Backfill(t.Context(), limited, limited)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Stopped || res.Refreshed != 1 || res.Remaining != 4 || res.Complete || len(calls) != 2 {
		t.Errorf("pass = %+v calls %d, want stopped after one re-read, four remaining", res, len(calls))
	}
}

// A failure waits in memory for the retry interval, then the item is read again.
func TestB4_FailureWaitsForTheRetry(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	flaky := addBelowGeneration(e, "Flaky", now.Add(-2*time.Hour))
	var calls []uuid.UUID
	ok := generationRefresh(e, &calls)
	failFirst := func(ctx context.Context, id uuid.UUID, v int) error {
		if id == flaky && len(calls) == 0 {
			calls = append(calls, id)
			return errors.New("network down")
		}
		return ok(ctx, id, v)
	}
	if res, _ := e.store.Backfill(t.Context(), failFirst, failFirst); res.Failed != 1 {
		t.Fatalf("first pass failed = %d, want 1", res.Failed)
	}
	calls = nil
	if res, _ := e.store.Backfill(t.Context(), ok, ok); res.Refreshed != 0 || len(calls) != 0 {
		t.Fatalf("pass inside the retry window re-read %d items", len(calls))
	}
	e.clock.Set(now.Add(25 * time.Hour))
	if res, _ := e.store.Backfill(t.Context(), ok, ok); res.Refreshed != 1 || len(calls) != 1 || calls[0] != flaky {
		t.Errorf("after the retry = %+v calls %v, want the flaky item re-read", res, calls)
	}
}

// Movies and TV items go to the scheduled refresh, books to the Open Library fill.
func TestB4_RoutesByProvider(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	movie := addBelowGeneration(e, "Movie", now.Add(-2*time.Hour))
	book := e.addItem("Book", map[string]any{"openlibrary_id": "OL27482W"})
	e.repo.edit(book, func(it *fakeItem) { it.typ = ptr("book") })
	e.repo.imports[book] = importSave{ItemID: book, Provider: ProviderOpenLibrary, SourceID: "OL27482W", Type: "book", ImportedFields: []byte("{}"), FetchedAt: now.Add(-time.Hour)}

	var tmdbCalls, olCalls []uuid.UUID
	res, err := e.store.Backfill(t.Context(), generationRefresh(e, &tmdbCalls), generationRefresh(e, &olCalls))
	if err != nil || res.Refreshed != 2 {
		t.Fatalf("pass = %+v err %v", res, err)
	}
	if len(tmdbCalls) != 1 || tmdbCalls[0] != movie || len(olCalls) != 1 || olCalls[0] != book {
		t.Errorf("tmdb %v, openlibrary %v: want the movie and the book routed apart", tmdbCalls, olCalls)
	}
}

// The completion is logged once: a later pass that finds nothing does not log it again.
func TestB4_CompletionIsReportedOnce(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	addBelowGeneration(e, "Movie", now.Add(-time.Hour))
	var calls []uuid.UUID
	r := generationRefresh(e, &calls)
	if res, _ := e.store.Backfill(t.Context(), r, r); !res.Complete {
		t.Fatal("first pass did not complete")
	}
	if res, _ := e.store.Backfill(t.Context(), r, r); res.Complete {
		t.Error("the completion was reported again")
	}
}

// BackfillFill (book step): the Open Library fill at the version the pass read, with the summary and
// without touching what the user set.
func TestB4_BookFillUsesTheOpenLibraryWorkAtTheReadVersion(t *testing.T) {
	book := items.Item{ID: uuid.New(), Type: ptr("book"), Version: 3, Title: "Mine", Metadata: []byte(`{"openlibrary_id":"OL27482W"}`)}
	client := &fakeClient{name: ProviderOpenLibrary, enabled: true, details: Details{
		SourceID: "OL27482W", Title: "The Hobbit", Fields: map[string]any{"pages": 310}, Synopsis: "A hobbit.",
	}}
	store := &fakeStore{}
	svc := newServiceWith(Providers{types.Book: client}, store, &fakeItems{item: book})
	if err := svc.BackfillFill(scope.With(t.Context(), uuid.New()), book.ID, 3); err != nil {
		t.Fatal(err)
	}
	p := store.applied
	if p == nil || p.Mode != ModeFill || p.ExpectedVersion != 3 || p.Synopsis != "A hobbit." || p.OriginalTitle != "Mine" {
		t.Fatalf("applied = %+v, want a fill at version 3 with the summary", p)
	}
	if client.detailsID != "OL27482W" {
		t.Errorf("details id = %q", client.detailsID)
	}
}

// A rate limit from the book provider reaches the pass as ErrProviderRateLimited (through fetchError).
func TestB4_BookFillKeepsTheRateLimitForThePass(t *testing.T) {
	book := items.Item{ID: uuid.New(), Type: ptr("book"), Version: 1, Metadata: []byte(`{"openlibrary_id":"OL27482W"}`)}
	client := &fakeClient{name: ProviderOpenLibrary, enabled: true, err: ErrProviderRateLimited}
	store := &fakeStore{}
	svc := newServiceWith(Providers{types.Book: client}, store, &fakeItems{item: book})
	err := svc.BackfillFill(scope.With(t.Context(), uuid.New()), book.ID, 1)
	if !stopsRun(err) || store.applied != nil {
		t.Fatalf("err = %v applied = %v, want a stopping rate limit and no write", err, store.applied)
	}
}
