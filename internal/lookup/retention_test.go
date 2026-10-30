package lookup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// importRow is a TMDB provenance row for the fake, with the given deadlines.
func importRow(itemID uuid.UUID, sourceID string, refreshAfter, expiresAt time.Time, fields string) importSave {
	fetched := refreshAfter.AddDate(0, -6, 0)
	return importSave{
		ItemID: itemID, Provider: ProviderTMDB, SourceID: sourceID, Type: "movie", ImportedFields: []byte(fields),
		OriginalTitle: "matrix", FetchedAt: fetched, RefreshAfter: &refreshAfter, ExpiresAt: &expiresAt,
	}
}

func TestT2b_MaintenancePicksOnlyDueTMDBRows(t *testing.T) {
	now := time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	due := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	expires := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)

	add := func(sourceID string, metadata map[string]any, refreshAfter time.Time) uuid.UUID {
		id := e.addItem("Matrix", metadata)
		e.repo.imports[id] = importRow(id, sourceID, refreshAfter, expires, "{}")
		return id
	}
	eligible := add("603", map[string]any{"tmdb_id": 603}, due)
	add("604", map[string]any{"tmdb_id": 604}, later) // not due yet
	deleted := add("605", map[string]any{"tmdb_id": 605}, due)
	e.repo.edit(deleted, func(it *fakeItem) { it.deleted = true })
	add("606", map[string]any{"tmdb_id": 606, "lookup_disabled": true}, due)
	retyped := add("607", map[string]any{"tmdb_id": 607}, due)
	e.repo.edit(retyped, func(it *fakeItem) { it.typ = ptr("tv_show") })

	var refreshed []uuid.UUID
	var versions []int
	result, err := e.store.Maintain(t.Context(), func(_ context.Context, itemID uuid.UUID, version int) error {
		refreshed = append(refreshed, itemID)
		versions = append(versions, version)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 1 || refreshed[0] != eligible {
		t.Fatalf("refreshed %v, want only %s (due, eligible)", refreshed, eligible)
	}
	if versions[0] != 1 {
		t.Errorf("refresh got version %d, want the item's version 1", versions[0])
	}
	if result != (MaintenanceResult{Refreshed: 1}) {
		t.Errorf("result = %+v, want one refresh", result)
	}
	if e.repo.commits != 0 {
		t.Errorf("a due pass with nothing expired committed %d transactions", e.repo.commits)
	}
}

func TestT2b_MaintenanceExpiresWithEditPreservation(t *testing.T) {
	now := time.Date(2026, time.August, 15, 9, 0, 0, 0, time.UTC)
	e := newApplyEnv(t, now)
	id := e.addItem("The Matrix", map[string]any{
		"release_year": 2000, "runtime_minutes": 136, "director": "Mine", "tmdb_id": 603,
	})
	expires := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	refreshAfter := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	row := importRow(id, "603", refreshAfter, expires,
		`{"release_year":1999,"runtime_minutes":136,"tmdb_id":603}`)
	title := "The Matrix"
	row.ImportedTitle = &title
	e.repo.imports[id] = row

	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error {
		t.Fatal("an expired import must not be refreshed")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != (MaintenanceResult{Expired: 1}) {
		t.Fatalf("result = %+v, want one expired", result)
	}
	if want := []string{"lock tree", "lock imported item", "update item", "delete import"}; !writesEqual(e.repo.writes, want) {
		t.Errorf("writes = %v, want %v", e.repo.writes, want)
	}
	stored := e.repo.items[id]
	if stored.title != "matrix" {
		t.Errorf("title = %q, want the original title restored", stored.title)
	}
	var metadata map[string]any
	if err := json.Unmarshal(stored.metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata) != 2 || metadata["release_year"] != float64(2000) || metadata["director"] != "Mine" {
		t.Errorf("metadata = %#v, want only the user's edit and own value", metadata)
	}
	if _, ok := e.repo.imports[id]; ok {
		t.Error("expired provenance still exists")
	}
}
