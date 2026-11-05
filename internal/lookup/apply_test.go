package lookup

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

func TestT2b_CreateAppliesItemAndProvenanceInOneTx(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix",
		CreateInput: &items.CreateInput{Title: "matrix", Metadata: map[string]any{"director": "My director"}},
		Fields:      map[string]any{"release_year": 1999, "director": "The Wachowskis"},
		PosterPath:  "/matrix.jpg", Poster: processedPoster("create"), RefreshPoster: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"lock tree", "insert attachment", "insert item", "save import"}
	if !writesEqual(e.repo.writes, want) || len(e.repo.outside) != 0 || e.repo.commits != 1 {
		t.Fatalf("writes = %v (outside tx %v), commits = %d; want %v in one commit", e.repo.writes, e.repo.outside, e.repo.commits, want)
	}
	stored := e.repo.items[it.ID]
	if stored.title != "The Matrix" || !stored.imageAttachment.Valid {
		t.Fatalf("stored item = %+v", stored)
	}
	metadata := decodeMetadata(t, stored.metadata)
	if metadata["director"] != "My director" || metadata["tmdb_id"] != float64(603) {
		t.Fatalf("metadata = %#v", metadata)
	}
	row, ok := e.repo.imports[it.ID]
	if !ok || row.Provider != ProviderTMDB || row.AttachmentID != stored.imageAttachment || row.ExpiresAt == nil {
		t.Fatalf("provenance = %+v ok=%v", row, ok)
	}
	if it.ID == uuid.Nil || it.Title != "The Matrix" {
		t.Errorf("returned item = %+v", it)
	}
	// The poster is kept after commit: no file is deleted, and two files remain on disk.
	if len(e.files.deletes) != 0 {
		t.Errorf("files deleted %+v, want none", e.files.deletes)
	}
	if n := countFiles(t, e); n != 2 {
		t.Errorf("files on disk = %d, want 2 (original and thumbnail)", n)
	}
}

func TestT2b_FailedCreateRollsBackAndRemovesPosterFilesAfterRollback(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	e.repo.failInsertItem = errors.New("insert refused")
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix", CreateInput: &items.CreateInput{Title: "matrix"},
		Fields: map[string]any{"release_year": 1999}, PosterPath: "/matrix.jpg", Poster: processedPoster("failed"),
	})
	if err == nil {
		t.Fatal("expected the insert to fail")
	}
	if len(e.repo.items) != 0 || len(e.repo.imports) != 0 || len(e.repo.attachments) != 0 || e.repo.commits != 0 {
		t.Fatalf("rollback left items=%d imports=%d attachments=%d commits=%d", len(e.repo.items), len(e.repo.imports), len(e.repo.attachments), e.repo.commits)
	}
	if len(e.files.deletes) != 2 {
		t.Fatalf("deletes = %+v, want the two prepared files", e.files.deletes)
	}
	for _, d := range e.files.deletes {
		if d.inTx {
			t.Errorf("file %s deleted inside the transaction", d.key)
		}
	}
	if n := countFiles(t, e); n != 0 {
		t.Errorf("files on disk = %d after rollback, want 0", n)
	}
}

func TestT2b_FillKeepsOwnedFields(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	id := e.addItem("Matrix", map[string]any{"director": "My director"})
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: id, ExpectedVersion: 1, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix",
		Fields: map[string]any{"release_year": 1999, "director": "The Wachowskis"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lock tree", "lock item", "update item", "save import"}; !writesEqual(e.repo.writes, want) || e.repo.commits != 1 {
		t.Fatalf("writes = %v commits = %d, want %v", e.repo.writes, e.repo.commits, want)
	}
	metadata := decodeMetadata(t, e.repo.items[id].metadata)
	if metadata["director"] != "My director" || metadata["release_year"] != float64(1999) || metadata["tmdb_id"] != float64(603) {
		t.Errorf("metadata after fill = %#v", metadata)
	}
	var snapshots map[string]any
	if err := json.Unmarshal(e.repo.imports[id].ImportedFields, &snapshots); err != nil {
		t.Fatal(err)
	}
	if _, owned := snapshots["director"]; owned {
		t.Errorf("user-owned director was claimed as imported: %#v", snapshots)
	}
	if snapshots["release_year"] != float64(1999) {
		t.Errorf("imported snapshots = %#v, want release_year", snapshots)
	}
	if it.ID != id {
		t.Errorf("returned item id = %s, want %s", it.ID, id)
	}
}

func TestT2b_RefreshRejectsStaleExpectedVersion(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	id := e.addItem("Matrix", nil)
	e.repo.edit(id, func(it *fakeItem) { it.version = 3 })
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeRematch, ItemID: id, ExpectedVersion: 2, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: map[string]any{"release_year": 1999},
		PosterPath: "/stale.jpg", Poster: processedPoster("stale"), RefreshPoster: true,
	})
	var conflict *apperr.VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 3 {
		t.Fatalf("error = %v, want version conflict at 3", err)
	}
	if want := []string{"lock tree", "lock item"}; !writesEqual(e.repo.writes, want) || e.repo.commits != 0 {
		t.Fatalf("writes = %v commits = %d: a stale apply must write nothing", e.repo.writes, e.repo.commits)
	}
	if _, ok := e.repo.imports[id]; ok {
		t.Error("provenance written for a stale apply")
	}
	if len(e.files.deletes) != 2 || e.files.deletes[0].inTx {
		t.Errorf("deletes = %+v, want the two prepared files removed after the rollback", e.files.deletes)
	}
}

func TestT2b_PosterDiscardedWhenItemHasImage(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	id := e.addItem("Matrix", nil)
	existing := uuid.New()
	e.repo.attachments[existing] = images.Attachment{ID: existing, StorageKey: "images/old.jpg", ThumbnailStorageKey: "images/old.thumb.jpg"}
	e.repo.edit(id, func(it *fakeItem) { it.imageAttachment = uuid.NullUUID{UUID: existing, Valid: true} })
	if _, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: id, ExpectedVersion: 1, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: map[string]any{"release_year": 1999},
		PosterPath: "/fill.jpg", Poster: processedPoster("fill"), RefreshPoster: true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, w := range e.repo.writes {
		if w == "insert attachment" {
			t.Fatalf("poster attached although the item has an image: writes %v", e.repo.writes)
		}
	}
	stored := e.repo.items[id]
	if !stored.imageAttachment.Valid || stored.imageAttachment.UUID != existing {
		t.Errorf("image = %+v, want the existing attachment kept", stored.imageAttachment)
	}
	if len(e.repo.attachments) != 1 {
		t.Errorf("attachment rows = %d, want the existing one only", len(e.repo.attachments))
	}
	if len(e.files.deletes) != 2 {
		t.Fatalf("deletes = %+v, want the discarded poster's two files", e.files.deletes)
	}
	for _, d := range e.files.deletes {
		if d.inTx || d.commits != 1 {
			t.Errorf("discarded file %s removed inTx=%v commits=%d, want after the commit", d.key, d.inTx, d.commits)
		}
	}
	if n := countFiles(t, e); n != 0 {
		t.Errorf("files on disk = %d, want 0", n)
	}
}

// F026-B2: an enriched create of a TMDB id already in the list is a duplicate.
func TestF026B2_ApplyCreateRejectsDuplicateTitle(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	e.repo.items[uuid.New()] = fakeItem{listID: e.list, title: "Matrix", metadata: []byte(`{"tmdb_id":603}`)}
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "matrix",
		CreateInput: &items.CreateInput{Title: "matrix"},
	})
	var c *apperr.ConflictError
	if !errors.As(err, &c) || c.Code != apperr.CodeDuplicateTitle {
		t.Fatalf("err = %v, want duplicate_title", err)
	}
}

// F026-B2: a remake with the same title but another TMDB id is allowed.
func TestF026B2_ApplyCreateAllowsSameTitleOtherTMDBID(t *testing.T) {
	e := newApplyEnv(t, time.Date(2026, time.March, 1, 10, 0, 0, 0, time.UTC))
	e.repo.items[uuid.New()] = fakeItem{listID: e.list, title: "Dune", metadata: []byte(`{"tmdb_id":841}`)}
	if _, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "438631",
		Title: "Dune", CreateInput: &items.CreateInput{Title: "Dune"},
	}); err != nil {
		t.Fatalf("remake: %v", err)
	}
	_, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeCreate, ListID: e.list, Type: "movie", Provider: ProviderTMDB, SourceID: "841",
		Title: "Dune (1984)", CreateInput: &items.CreateInput{Title: "dune 84"},
	})
	var c *apperr.ConflictError
	if !errors.As(err, &c) || c.Code != apperr.CodeDuplicateTitle {
		t.Fatalf("same tmdb id: err = %v, want duplicate_title", err)
	}
}
