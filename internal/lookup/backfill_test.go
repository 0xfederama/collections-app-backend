package lookup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/tmdb"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// fakeLinks is a fake TMDB watch/providers endpoint keyed by source id.
type fakeLinks struct {
	enabled bool
	links   map[string]string // source id -> link ("" = TMDB has none)
	errs    map[string]error
	calls   []string
}

func (f *fakeLinks) Enabled() bool { return f.enabled }
func (f *fakeLinks) WatchLink(_ context.Context, _ string, id int, _ string) (string, error) {
	key := itoa(id)
	f.calls = append(f.calls, key)
	if err := f.errs[key]; err != nil {
		return "", err
	}
	return f.links[key], nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// oldItem is a movie looked up before api 0.14.0: tmdb_id and platforms, no link.
func (e *lookupEnv) oldItem(title, tmdbID string) items.Item {
	e.t.Helper()
	it := e.create(title, nil)
	it, err := e.store.Apply(e.t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: tmdbID,
		Title: title + " (TMDB)", OriginalTitle: title, Fields: streamingFields(""),
		PosterPath: "/p.jpg", Poster: processedPoster(tmdbID), RefreshPoster: true,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return it
}

func (e *lookupEnv) snapshot(it items.Item, key string) (any, bool) {
	e.t.Helper()
	r, _, err := e.store.Read(e.t.Context(), it.ID)
	if err != nil {
		e.t.Fatal(err)
	}
	v, ok := r.ImportedFields[key]
	return v, ok
}

const link603 = "https://www.justwatch.com/it/movie/the-matrix"

func TestBackfill_FillsOnlyTheLinkAndNothingElse(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	it := e.oldItem("Matrix", "603")
	// The user edited the platforms after the import: that edit must survive.
	it, err := e.items.Update(t.Context(), it.ID, it.Version, items.Patch{Metadata: map[string]any{
		"release_year": 1999, "tmdb_id": 603, "streaming": map[string]any{"country": "IT", "platforms": []any{"My Plex"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := e.store.Read(t.Context(), it.ID)
	f := &fakeLinks{enabled: true, links: map[string]string{"603": link603}}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "it")
	if err != nil || res.Updated != 1 || res.Scanned != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	got, _ := e.items.Get(t.Context(), it.ID)
	meta := decodeMetadata(t, got.Metadata)
	if linkOf(t, got.Metadata) != link603 {
		t.Fatalf("link = %v", linkOf(t, got.Metadata))
	}
	s := meta["streaming"].(map[string]any)
	if got.Title != it.Title || got.Image == nil || s["platforms"].([]any)[0] != "My Plex" || meta["release_year"] != float64(1999) || meta["tmdb_id"] != float64(603) {
		t.Errorf("other values changed: title=%q image=%v metadata=%v", got.Title, got.Image, meta)
	}
	if got.Version != it.Version || !got.UpdatedAt.Equal(it.UpdatedAt) {
		t.Errorf("version/updated_at = %d/%s, want unchanged %d/%s", got.Version, got.UpdatedAt, it.Version, it.UpdatedAt)
	}
	after, _, _ := e.store.Read(t.Context(), it.ID)
	if after.ImportedFields["streaming.link"] != link603 || !after.FetchedAt.Equal(before.FetchedAt) || after.AttachmentID == nil {
		t.Errorf("provenance = %+v", after)
	}
	for k, v := range before.ImportedFields {
		if _, ok := after.ImportedFields[k]; !ok {
			t.Errorf("snapshot %s=%v lost", k, v)
		}
	}
	// Idempotent: a second pass finds nothing and calls nothing.
	f.calls = nil
	res, err = e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res != (BackfillResult{}) || len(f.calls) != 0 {
		t.Fatalf("second pass res=%+v calls=%v err=%v", res, f.calls, err)
	}
}

func TestBackfill_SkipsLookupDisabledTrashedOtherCountryAndNoPlatforms(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	disabled := e.oldItem("Disabled", "1")
	trashed := e.oldItem("Trashed", "2")
	other := e.oldItem("Other country", "3")
	bare := e.oldItem("No platforms", "4")
	var batch uuid.UUID
	if err := e.db.QueryRow(t.Context(), `INSERT INTO trash_batches (workspace_id, root_type, root_id, root_name, former_path) VALUES ($1, 'item', $2, 'Trashed', '[]') RETURNING id`, e.ws, trashed.ID).Scan(&batch); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET deleted_at = now(), trash_batch_id = $2 WHERE id = $1`, trashed.ID, batch); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql string
		id  uuid.UUID
	}{
		{`UPDATE items SET metadata = metadata || '{"lookup_disabled": true}' WHERE id = $1`, disabled.ID},
		{`UPDATE items SET metadata = jsonb_set(metadata, '{streaming,country}', '"GB"') WHERE id = $1`, other.ID},
		{`UPDATE items SET metadata = metadata #- '{streaming,platforms}' WHERE id = $1`, bare.ID},
	} {
		if _, err := e.db.Exec(t.Context(), q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeLinks{enabled: true, links: map[string]string{"1": link603, "2": link603, "3": link603, "4": link603}}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res != (BackfillResult{}) || len(f.calls) != 0 {
		t.Fatalf("res=%+v calls=%v err=%v, want nothing scanned", res, f.calls, err)
	}
	// A restored item is unmarked, so the next pass picks it up.
	if _, err := e.db.Exec(t.Context(), `UPDATE items SET deleted_at = NULL, trash_batch_id = NULL WHERE id = $1`, trashed.ID); err != nil {
		t.Fatal(err)
	}
	res, err = e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res.Updated != 1 || len(f.calls) != 1 || f.calls[0] != "2" {
		t.Fatalf("after restore res=%+v calls=%v err=%v", res, f.calls, err)
	}
}

func TestBackfill_NoLinkAndNotFoundRecordTheCheckAndAreNotRetried(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	none := e.oldItem("No link", "10")
	gone := e.oldItem("Gone", "11")
	foreign := e.oldItem("Foreign", "12")
	f := &fakeLinks{enabled: true, links: map[string]string{"10": "", "12": ""}, errs: map[string]error{"11": tmdb.ErrNotFound}}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res.Scanned != 3 || res.NoLink != 3 || res.Updated != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, it := range []items.Item{none, gone, foreign} {
		if v, ok := e.snapshot(it, "streaming.link"); !ok || v != "none:IT" {
			t.Errorf("%s marker = %v, %v", it.Title, v, ok)
		}
		got, _ := e.items.Get(t.Context(), it.ID)
		if _, has := decodeMetadata(t, got.Metadata)["streaming"].(map[string]any)["link"]; has || got.Version != it.Version {
			t.Errorf("%s: metadata/version changed: %s v%d", it.Title, got.Metadata, got.Version)
		}
	}
	f.calls = nil
	res, err = e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res != (BackfillResult{}) || len(f.calls) != 0 {
		t.Fatalf("second pass res=%+v calls=%v err=%v", res, f.calls, err)
	}
	// Changing the streaming country clears the marker (settings tx), so the item is asked again.
	svc := workspace.NewService(e.db.(*pgxpool.Pool))
	gb := "GB"
	if _, err := svc.Update(t.Context(), e.ws, 1, workspace.SettingsPatch{StreamingCountryCode: &gb}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.snapshot(none, "streaming.link"); ok {
		t.Error("marker survived a country change")
	}
}

func TestBackfill_TransientErrorLeavesItemUnmarkedAndRateLimitStopsThePass(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	a := e.oldItem("A", "21")
	b := e.oldItem("B", "22")
	c := e.oldItem("C", "23")
	f := &fakeLinks{enabled: true, links: map[string]string{"21": link603, "22": link603, "23": link603},
		errs: map[string]error{"21": errors.New("TMDB request failed (HTTP 500)"), "22": errors.New("TMDB request failed (HTTP 500)"), "23": errors.New("TMDB request failed (HTTP 500)")}}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res.Errors != 3 || res.Updated != 0 || res.Scanned != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	for _, it := range []items.Item{a, b, c} {
		if _, ok := e.snapshot(it, "streaming.link"); ok {
			t.Errorf("%s was marked after a transient error", it.Title)
		}
	}
	// Next startup: TMDB answers 429 on the first call, so the pass stops there.
	f = &fakeLinks{enabled: true, errs: map[string]error{"21": tmdb.ErrRateLimited, "22": tmdb.ErrRateLimited, "23": tmdb.ErrRateLimited}}
	res, err = e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || len(f.calls) != 1 || res.Updated != 0 || res.Errors != 0 {
		t.Fatalf("429: res=%+v calls=%v err=%v", res, f.calls, err)
	}
	// And a healthy pass finally completes all three.
	f = &fakeLinks{enabled: true, links: map[string]string{"21": link603, "22": link603, "23": link603}}
	res, err = e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res.Updated != 3 {
		t.Fatalf("recovery: res=%+v err=%v", res, err)
	}
}

func TestBackfill_ConsecutiveErrorsEndThePassEarly(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	f := &fakeLinks{enabled: true, errs: map[string]error{}}
	for i := 0; i < backfillMaxConsecutiveErrors+2; i++ {
		id := itoa(100 + i)
		e.oldItem("T"+id, id)
		f.errs[id] = errors.New("TMDB request failed")
	}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res.Errors != backfillMaxConsecutiveErrors || len(f.calls) != backfillMaxConsecutiveErrors {
		t.Fatalf("res=%+v calls=%d err=%v", res, len(f.calls), err)
	}
}

func TestBackfill_NoKeyIsANoOp(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	it := e.oldItem("Matrix", "603")
	f := &fakeLinks{enabled: false, links: map[string]string{"603": link603}}
	e.store.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.store.RunStreamingLinkBackfill(t.Context(), f, fakeSettings{workspace.Settings{StreamingCountryCode: "IT"}})
	e.store.RunStreamingLinkBackfill(t.Context(), nil, fakeSettings{workspace.Settings{StreamingCountryCode: "IT"}})
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
	if _, ok := e.snapshot(it, "streaming.link"); ok {
		t.Error("marked without a key")
	}
	// Enabled but with no streaming country: also nothing.
	f.enabled = true
	e.store.RunStreamingLinkBackfill(t.Context(), f, fakeSettings{workspace.Settings{}})
	if len(f.calls) != 0 {
		t.Fatalf("calls without country = %v", f.calls)
	}
	// Enabled with a country: the wrapper does the pass.
	e.store.RunStreamingLinkBackfill(t.Context(), f, fakeSettings{workspace.Settings{StreamingCountryCode: "IT"}})
	if got, _ := e.items.Get(t.Context(), it.ID); linkOf(t, got.Metadata) != link603 {
		t.Errorf("wrapper did not fill the link: %s", got.Metadata)
	}
}

func TestBackfill_PanicIsRecovered(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	e.oldItem("Matrix", "603")
	e.store.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	e.store.RunStreamingLinkBackfill(t.Context(), panicLinks{}, fakeSettings{workspace.Settings{StreamingCountryCode: "IT"}})
}

type panicLinks struct{}

func (panicLinks) Enabled() bool { return true }
func (panicLinks) WatchLink(context.Context, string, int, string) (string, error) {
	panic("boom")
}

func TestBackfill_CancelledContextStopsThePass(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	e.oldItem("Matrix", "603")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := e.store.BackfillStreamingLinks(ctx, &fakeLinks{enabled: true}, "IT"); err == nil {
		t.Fatal("want an error from a cancelled context")
	}
}

func TestBackfill_NeverOverridesAnExistingLinkOrMarker(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(matrixLink),
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeLinks{enabled: true, links: map[string]string{"603": link603}}
	res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT")
	if err != nil || res != (BackfillResult{}) || len(f.calls) != 0 {
		t.Fatalf("res=%+v calls=%v err=%v", res, f.calls, err)
	}
	if linkOf(t, it.Metadata) != matrixLink {
		t.Errorf("link = %v", linkOf(t, it.Metadata))
	}
}

// The regular owned refresh already fills a missing link as an imported leaf, so
// the backfill needs no counterpart there (and a "none" marker never blocks it).
func TestBackfill_RegularRefreshFillsAMissingLinkAndDropsTheMarker(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.oldItem("Matrix", "603")
	f := &fakeLinks{enabled: true, links: map[string]string{"603": ""}}
	if res, err := e.store.BackfillStreamingLinks(t.Context(), f, "IT"); err != nil || res.NoLink != 1 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	e.clock.Set(start.AddDate(0, 6, -1)) // past refresh_after (5 months), before expiry
	refreshed := map[string]any{"release_year": 1999, "streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix"}, "link": link603}}
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "Matrix (TMDB)", OriginalTitle: "Matrix", Fields: refreshed, RefreshFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if linkOf(t, it.Metadata) != link603 {
		t.Fatalf("link after refresh = %v", linkOf(t, it.Metadata))
	}
	if v, _ := e.snapshot(it, "streaming.link"); v != link603 {
		t.Errorf("snapshot = %v, want the link (marker replaced)", v)
	}
}
