package lookup

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/items/core"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

const matrixLink = "https://www.justwatch.com/it/movie/the-matrix"

func streamingFields(link string) map[string]any {
	s := map[string]any{"country": "IT", "platforms": []any{"Netflix"}}
	if link != "" {
		s["link"] = link
	}
	return map[string]any{"release_year": 1999, "streaming": s}
}

func linkOf(t *testing.T, raw []byte) any {
	t.Helper()
	s, _ := decodeMetadata(t, raw)["streaming"].(map[string]any)
	return s["link"]
}

func TestStreamingLink_TrackedKeptOnPlatformEditAndClientCannotWriteIt(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(matrixLink),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != matrixLink {
		t.Fatalf("link after fill = %v", got)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if record.ImportedFields["streaming.link"] != matrixLink {
		t.Errorf("snapshots = %v, want streaming.link tracked", record.ImportedFields)
	}

	// Editing only the platforms keeps the link; a link sent by the client is ignored.
	it, err = e.items.Update(scope.With(t.Context(), e.ws), it.ID, it.Version, core.Patch{Metadata: map[string]any{
		"release_year": 1999,
		"streaming":    map[string]any{"country": "IT", "platforms": []any{"Netflix", "Prime Video"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != matrixLink {
		t.Errorf("link after platform edit = %v, want kept", got)
	}
	it, err = e.items.Update(scope.With(t.Context(), e.ws), it.ID, it.Version, core.Patch{Metadata: map[string]any{
		"streaming": map[string]any{"country": "IT", "platforms": []any{"Netflix"}, "link": "https://evil.example/"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != matrixLink {
		t.Errorf("link after client-sent link = %v, want the stored one", got)
	}
	// Another country is another page: the stored link does not carry over.
	it, err = e.items.Update(scope.With(t.Context(), e.ws), it.ID, it.Version, core.Patch{Metadata: map[string]any{
		"streaming": map[string]any{"country": "GB", "platforms": []any{"Netflix"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != nil {
		t.Errorf("link after country edit = %v, want none", got)
	}

	// A client cannot plant a link on create either.
	c, err := e.items.Create(scope.With(t.Context(), e.ws), e.list, core.CreateInput{Title: "Other", Metadata: map[string]any{
		"streaming": map[string]any{"country": "IT", "link": matrixLink},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, c.Metadata); got != nil {
		t.Errorf("link on client create = %v, want none", got)
	}
}

func TestStreamingLink_RefreshReplacesAndExpiryDropsItWithTheImportedValues(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	e := newLookupEnv(t, start)
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(matrixLink),
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh := "https://www.justwatch.com/it/movie/matrix-2"
	it, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(fresh), RefreshFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != fresh {
		t.Fatalf("link after refresh = %v", got)
	}
	// A refresh with no link removes the stale one.
	it, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(""), RefreshFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := linkOf(t, it.Metadata); got != nil {
		t.Fatalf("link after refresh without link = %v", got)
	}
	it, err = e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeOwned, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(fresh), RefreshFields: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Retention: expiry removes the link with the other unchanged imported values.
	e.clock.Set(start.AddDate(1, 0, 0))
	result, err := e.store.Maintain(t.Context(), func(context.Context, uuid.UUID, int) error { return nil })
	if err != nil || result.Expired != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got, _ := e.items.Get(scope.With(t.Context(), e.ws), it.ID)
	s, _ := decodeMetadata(t, got.Metadata)["streaming"].(map[string]any)
	if _, has := s["link"]; has || s["country"] != "IT" {
		t.Errorf("streaming after expiry = %v, want the link gone and the country kept", s)
	}
}

func TestStreamingLink_CountryChangeClearsItemsAndSnapshots(t *testing.T) {
	e := newLookupEnv(t, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	it := e.create("Matrix", nil)
	it, err := e.store.Apply(t.Context(), ApplyParams{
		Mode: ModeFill, ItemID: it.ID, ExpectedVersion: it.Version, Type: "movie", Provider: ProviderTMDB, SourceID: "603",
		Title: "The Matrix", OriginalTitle: "Matrix", Fields: streamingFields(matrixLink),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := workspace.NewService(workspace.NewPostgres(e.db.(*pgxpool.Pool)))
	// A change that is not the country keeps the link.
	name := "Renamed"
	if _, err := svc.Update(scope.With(t.Context(), e.ws), 1, workspace.SettingsPatch{WorkspaceName: &name}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.items.Get(scope.With(t.Context(), e.ws), it.ID)
	if linkOf(t, got.Metadata) != matrixLink {
		t.Fatal("link cleared by a change of the name")
	}
	gb := "GB"
	if _, err := svc.Update(scope.With(t.Context(), e.ws), 2, workspace.SettingsPatch{StreamingCountryCode: &gb}); err != nil {
		t.Fatal(err)
	}
	got, _ = e.items.Get(scope.With(t.Context(), e.ws), it.ID)
	s, _ := decodeMetadata(t, got.Metadata)["streaming"].(map[string]any)
	if _, has := s["link"]; has || s["platforms"] == nil {
		t.Errorf("streaming after country change = %v, want the link gone and the platforms kept", s)
	}
	record, _, _ := e.store.Read(t.Context(), it.ID)
	if _, has := record.ImportedFields["streaming.link"]; has {
		t.Errorf("snapshots = %v, want streaming.link cleared", record.ImportedFields)
	}
	if got.Version != it.Version {
		t.Errorf("version = %d, want %d: derived data, no bump", got.Version, it.Version)
	}
}
