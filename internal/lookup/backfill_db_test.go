package lookup

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/items"
)

// F-028 B4 (D-164) on Postgres: the selection (oldest fetch first, trashed and lookup_disabled skipped),
// the progress columns and the idempotent second pass.
func TestB4_DB_SelectionOrdersByFetchAndSkipsTrashedAndDisabled(t *testing.T) {
	now := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, now)
	var ids []uuid.UUID
	for i, sid := range []string{"601", "602", "603", "604"} {
		it, err := e.store.Apply(t.Context(), ApplyParams{
			Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: sid,
			Title: "Movie " + sid, OriginalTitle: "Movie", CreateInput: &items.CreateInput{Title: "Movie " + sid},
			Fields: map[string]any{"release_year": 1999}, HistoryLimit: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
		// Item 0 is the most recently fetched; item 3 the oldest.
		if _, err := e.db.Exec(t.Context(), `UPDATE lookup_imports SET import_generation = 0, fetched_at = $2 WHERE item_id = $1`,
			it.ID, now.Add(-time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// Trashed: deleted_at and a trash batch, as the trash flow sets them (items_trash_check).
	batch := uuid.New()
	if _, err := e.db.Exec(t.Context(), `INSERT INTO trash_batches (id, workspace_id, root_type, root_id, root_name, former_path)
		VALUES ($1, $2, 'item', $3, 'Movie 602', '[]')`, batch, e.ws, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, ids[1], batch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET metadata = metadata || '{"lookup_disabled": true}' WHERE id = $1`, ids[2]); err != nil {
		t.Fatal(err)
	}

	var calls []uuid.UUID
	stub := func(_ context.Context, id uuid.UUID, _ int) error {
		calls = append(calls, id)
		_, err := e.db.Exec(t.Context(), `UPDATE lookup_imports SET import_generation = $2 WHERE item_id = $1`, id, currentImportGeneration)
		return err
	}
	res, err := e.store.Backfill(t.Context(), stub, stub)
	if err != nil {
		t.Fatal(err)
	}
	// Eligible: item 0 and item 3 (1 and 2 are trashed or disabled). Item 3 is the older fetch.
	if res.Refreshed != 2 || res.Remaining != 0 || !res.Complete {
		t.Fatalf("pass = %+v, want two re-reads, none remaining, complete", res)
	}
	if len(calls) != 2 || calls[0] != ids[3] || calls[1] != ids[0] {
		t.Errorf("calls = %v, want the older fetch (item 3) first", calls)
	}

	// The skipped rows stay below the generation.
	for _, i := range []int{1, 2} {
		var gen int
		if err := e.db.QueryRow(t.Context(), `SELECT import_generation FROM lookup_imports WHERE item_id = $1`, ids[i]).Scan(&gen); err != nil {
			t.Fatal(err)
		}
		if gen != 0 {
			t.Errorf("skipped item %d generation = %d, want 0", i, gen)
		}
	}

	// A second pass finds nothing and calls nothing.
	calls = nil
	again, err := e.store.Backfill(t.Context(), stub, stub)
	if err != nil || again.Refreshed != 0 || again.Complete || len(calls) != 0 {
		t.Errorf("second pass = %+v err %v calls %v, want a no-op", again, err, calls)
	}
}
