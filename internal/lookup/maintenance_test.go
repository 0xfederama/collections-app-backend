package lookup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
)

// addTMDB puts a movie with TMDB provenance fetched at fetchedAt (nil: no provenance yet).
func addTMDB(e *applyEnv, title string, metadata map[string]any, fetchedAt *time.Time) uuid.UUID {
	id := e.addItem(title, metadata)
	if fetchedAt != nil {
		markFetched(e, id, *fetchedAt)
	}
	return id
}

// markFetched records a TMDB import of the item at at, as a successful refresh does.
func markFetched(e *applyEnv, id uuid.UUID, at time.Time) {
	e.repo.imports[id] = importSave{
		ItemID: id, Provider: ProviderTMDB, SourceID: "1", Type: "movie", ImportedFields: []byte("{}"),
		OriginalTitle: "original", FetchedAt: at,
	}
}

func ago(now time.Time, d time.Duration) *time.Time {
	t := now.Add(-d)
	return &t
}

// refreshOK is a refresh that succeeds and records the fetch at the fake clock's time.
func refreshOK(e *applyEnv, calls *[]uuid.UUID) RefreshFunc {
	return func(_ context.Context, id uuid.UUID, _ int) error {
		*calls = append(*calls, id)
		markFetched(e, id, e.clock.Now())
		return nil
	}
}

func TestWeekly_PicksOnlyDueMovieAndTVItems(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	never := addTMDB(e, "Never fetched", map[string]any{"tmdb_id": 604}, nil)
	due := addTMDB(e, "Due", map[string]any{"tmdb_id": 603}, ago(now, 8*24*time.Hour))
	addTMDB(e, "Not yet", map[string]any{"tmdb_id": 605}, ago(now, 6*24*time.Hour))
	trashed := addTMDB(e, "Trashed", map[string]any{"tmdb_id": 606}, ago(now, 30*24*time.Hour))
	e.repo.edit(trashed, func(it *fakeItem) { it.deleted = true })
	addTMDB(e, "Lookup off", map[string]any{"tmdb_id": 607, "lookup_disabled": true}, ago(now, 30*24*time.Hour))
	book := addTMDB(e, "Book", map[string]any{"tmdb_id": 608}, ago(now, 30*24*time.Hour))
	e.repo.edit(book, func(it *fakeItem) { it.typ = ptr("book") })
	addTMDB(e, "No id", map[string]any{"release_year": 1999}, ago(now, 30*24*time.Hour))
	addTMDB(e, "Bad id", map[string]any{"tmdb_id": "603"}, ago(now, 30*24*time.Hour))
	tv := addTMDB(e, "Series", map[string]any{"tmdb_id": 609}, ago(now, 9*24*time.Hour))
	e.repo.edit(tv, func(it *fakeItem) { it.typ = ptr("tv_show") })

	var calls []uuid.UUID
	result, err := e.store.Maintain(t.Context(), refreshOK(e, &calls))
	if err != nil {
		t.Fatal(err)
	}
	// A never-fetched item (typed id) is due at once and goes first; then the oldest
	// refresh (the series, fetched 9 days ago, before the movie fetched 8 days ago).
	if len(calls) != 3 || calls[0] != never || calls[1] != tv || calls[2] != due {
		t.Fatalf("refreshed %v, want the never-fetched, the older series and the movie in that order", calls)
	}
	if result != (MaintenanceResult{Refreshed: 3}) {
		t.Errorf("result = %+v, want three refreshes", result)
	}
}

func TestWeekly_BatchIsBoundedAndSpreadsAcrossRuns(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	for i := 0; i < 25; i++ {
		addTMDB(e, "Movie", map[string]any{"tmdb_id": 1000 + i}, ago(now, 30*24*time.Hour))
	}
	var calls []uuid.UUID
	for run, want := range []int{10, 10, 5, 0} {
		calls = nil
		result, err := e.store.Maintain(t.Context(), refreshOK(e, &calls))
		if err != nil {
			t.Fatal(err)
		}
		if len(calls) != want || result.Refreshed != want {
			t.Fatalf("run %d refreshed %d (result %+v), want %d", run+1, len(calls), result, want)
		}
	}
}

func TestWeekly_NotDueAgainWithinTheWeek(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	addTMDB(e, "Movie", map[string]any{"tmdb_id": 603}, ago(now, 30*24*time.Hour))
	var calls []uuid.UUID
	if _, err := e.store.Maintain(t.Context(), refreshOK(e, &calls)); err != nil || len(calls) != 1 {
		t.Fatalf("first run: calls=%d err=%v", len(calls), err)
	}
	calls = nil
	e.clock.Set(now.Add(6 * 24 * time.Hour))
	if _, err := e.store.Maintain(t.Context(), refreshOK(e, &calls)); err != nil || len(calls) != 0 {
		t.Fatalf("six days later: calls=%d err=%v", len(calls), err)
	}
	e.clock.Set(now.Add(7 * 24 * time.Hour))
	if _, err := e.store.Maintain(t.Context(), refreshOK(e, &calls)); err != nil || len(calls) != 1 {
		t.Fatalf("seven days later: calls=%d err=%v", len(calls), err)
	}
}

func TestWeekly_FailuresNeverWriteAndAreRetried(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		err   error
		retry time.Duration // the first retry; the item is skipped until then
	}{
		{name: "TMDB unreachable or 5xx", err: errors.New("TMDB request failed"), retry: 24 * time.Hour},
		{name: "TMDB 404 for the id", err: ErrMatchNotFound, retry: 7 * 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newApplyEnv(t, now)
			fetched := ago(now, 30*24*time.Hour)
			id := addTMDB(e, "Movie", map[string]any{"release_year": 1999, "tmdb_id": 603}, fetched)
			calls := 0
			fail := func(context.Context, uuid.UUID, int) error {
				calls++
				return tc.err
			}
			result, err := e.store.Maintain(t.Context(), fail)
			if err != nil || result.Failed != 1 || calls != 1 {
				t.Fatalf("first run: result=%+v calls=%d err=%v", result, calls, err)
			}
			if len(e.repo.writes) != 0 {
				t.Errorf("a failed refresh wrote %v", e.repo.writes)
			}
			if it := e.repo.items[id]; it.version != 1 || it.title != "Movie" {
				t.Errorf("a failed refresh changed the item: %+v", it)
			}
			if row := e.repo.imports[id]; !row.FetchedAt.Equal(*fetched) {
				t.Errorf("a failed refresh moved the fetch time to %s", row.FetchedAt)
			}
			e.clock.Set(now.Add(tc.retry - time.Minute))
			if _, err := e.store.Maintain(t.Context(), fail); err != nil || calls != 1 {
				t.Fatalf("before the retry time: calls=%d err=%v", calls, err)
			}
			e.clock.Set(now.Add(tc.retry))
			if _, err := e.store.Maintain(t.Context(), fail); err != nil || calls != 2 {
				t.Fatalf("at the retry time: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWeekly_RateLimitStopsTheRun(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	for i := 0; i < 3; i++ {
		addTMDB(e, "Movie", map[string]any{"tmdb_id": 1000 + i}, ago(now, 30*24*time.Hour))
	}
	calls := 0
	limited := func(context.Context, uuid.UUID, int) error {
		calls++
		return ErrProviderRateLimited
	}
	result, err := e.store.Maintain(t.Context(), limited)
	if err != nil || !result.Stopped || calls != 1 || result.Failed != 0 {
		t.Fatalf("result=%+v calls=%d err=%v, want one call and a stopped run", result, calls, err)
	}
	// A 429 is not the item's failure: nothing is deferred, the next run tries it again.
	if _, err := e.store.Maintain(t.Context(), limited); err != nil || calls != 2 {
		t.Fatalf("next run: calls=%d err=%v", calls, err)
	}
}

func TestWeekly_DisabledLookupStopsWithoutWrites(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	addTMDB(e, "Movie", map[string]any{"tmdb_id": 603}, ago(now, 30*24*time.Hour))
	addTMDB(e, "Movie two", map[string]any{"tmdb_id": 604}, ago(now, 30*24*time.Hour))
	calls := 0
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		calls++
		return apperr.LookupDisabled()
	})
	if err != nil || !result.Stopped || calls != 1 || len(e.repo.writes) != 0 {
		t.Fatalf("result=%+v calls=%d writes=%v err=%v", result, calls, e.repo.writes, err)
	}
}

func TestWeekly_ChangedOrTrashedItemStaysDue(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	first := addTMDB(e, "Movie", map[string]any{"tmdb_id": 603}, ago(now, 30*24*time.Hour))
	second := addTMDB(e, "Movie two", map[string]any{"tmdb_id": 604}, ago(now, 30*24*time.Hour))
	var calls, refreshed []uuid.UUID
	result, err := e.store.Maintain(t.Context(), func(_ context.Context, id uuid.UUID, _ int) error {
		calls = append(calls, id)
		if id == first {
			return apperr.VersionConflict(2)
		}
		refreshed = append(refreshed, id)
		markFetched(e, id, now)
		return nil
	})
	if err != nil || result.Refreshed != 1 || result.Failed != 0 || result.Stopped {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(calls) != 2 || len(refreshed) != 1 || refreshed[0] != second {
		t.Fatalf("calls = %v refreshed = %v", calls, refreshed)
	}
	// Not deferred: the same run retries it once the next pass reads the new version.
	calls = nil
	if _, err := e.store.Maintain(t.Context(), func(_ context.Context, id uuid.UUID, _ int) error {
		calls = append(calls, id)
		return errNotScheduled
	}); err != nil || len(calls) != 1 || calls[0] != first {
		t.Fatalf("next pass calls = %v err = %v", calls, err)
	}
}
