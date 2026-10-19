package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// fakeClient is a fake Provider. name defaults to the TMDB provider.
type fakeClient struct {
	name           string
	enabled        bool
	matches        []Suggestion
	details        Details
	err            error
	downloads      int
	searchType     string
	searchQuery    string
	searchLanguage string
	detailsID      string
}

func (f *fakeClient) Name() string {
	if f.name == "" {
		return ProviderTMDB
	}
	return f.name
}
func (f *fakeClient) Enabled() bool { return f.enabled }
func (f *fakeClient) ValidateSourceID(id string) error {
	return ValidSourceID(f.Name(), id)
}
func (f *fakeClient) Search(_ context.Context, typ, query, language string) ([]Suggestion, error) {
	f.searchType, f.searchQuery, f.searchLanguage = typ, query, language
	return f.matches, f.err
}
func (f *fakeClient) Details(_ context.Context, _, id, _, _ string) (Details, error) {
	f.detailsID = id
	return f.details, f.err
}
func (f *fakeClient) DownloadImage(context.Context, string) ([]byte, error) {
	f.downloads++
	return nil, f.err
}

type fakeStore struct {
	typ     string
	record  Record
	found   bool
	applied *ApplyParams
	result  items.Item
	err     error
}

func (f *fakeStore) ListType(context.Context, uuid.UUID) (string, error) { return f.typ, f.err }
func (f *fakeStore) Read(context.Context, uuid.UUID) (Record, bool, error) {
	return f.record, f.found, f.err
}
func (f *fakeStore) Apply(_ context.Context, p ApplyParams) (items.Item, error) {
	f.applied = &p
	return f.result, f.err
}

type fakeItems struct {
	item        items.Item
	created     *items.CreateInput
	updateCalls int
	err         error
}

func (f *fakeItems) Get(context.Context, uuid.UUID) (items.Item, error) { return f.item, f.err }
func (f *fakeItems) Create(_ context.Context, _ uuid.UUID, in items.CreateInput) (items.Item, error) {
	f.created = &in
	return f.item, f.err
}
func (f *fakeItems) Update(_ context.Context, _ uuid.UUID, _ int, _ items.Patch) (items.Item, error) {
	f.updateCalls++
	return f.item, f.err
}

type fakeSettings struct{ value workspace.Settings }

func (f fakeSettings) Get(context.Context, uuid.UUID) (workspace.Settings, error) {
	return f.value, nil
}

type testClock struct{ now time.Time }

func (c testClock) Now() time.Time { return c.now }

func newService(client *fakeClient, store *fakeStore, itemSvc *fakeItems) *Service {
	return newServiceWith(Providers{"movie": client, "tv_show": client}, store, itemSvc)
}

func newServiceWith(providers Providers, store *fakeStore, itemSvc *fakeItems) *Service {
	return New(providers, store, itemSvc, fakeSettings{workspace.Settings{
		MetadataLanguage: "it-IT", StreamingCountryCode: "IT", PreferredStores: []string{"kobo"},
	}}, testClock{now: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}, uuid.New())
}

func TestCreateDefaultLookupDegradesButExplicitLookupFails(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lookup   *bool
		client   *fakeClient
		wantCode string
	}{
		{name: "default missing key", client: &fakeClient{}},
		{name: "default upstream failure", client: &fakeClient{enabled: true, err: errors.New("down")}},
		{name: "explicit missing key", lookup: boolPtr(true), client: &fakeClient{}, wantCode: "disabled"},
		{name: "explicit upstream failure", lookup: boolPtr(true), client: &fakeClient{enabled: true, err: errors.New("down")}, wantCode: "upstream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{typ: "movie"}
			itemSvc := &fakeItems{item: items.Item{Title: "Typed title"}}
			_, err := newService(tc.client, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Typed title"}, CreateOptions{Lookup: tc.lookup})
			switch tc.wantCode {
			case "disabled":
				var target *apperr.LookupDisabledError
				if !errors.As(err, &target) || itemSvc.created != nil {
					t.Fatalf("err=%v created=%v", err, itemSvc.created)
				}
			case "upstream":
				var target *apperr.UpstreamUnavailableError
				if !errors.As(err, &target) || itemSvc.created != nil {
					t.Fatalf("err=%v created=%v", err, itemSvc.created)
				}
			default:
				if err != nil || itemSvc.created == nil || itemSvc.created.Title != "Typed title" || store.applied != nil {
					t.Fatalf("err=%v created=%v applied=%v", err, itemSvc.created, store.applied)
				}
			}
		})
	}
}

func TestCreateOptOutStoresLookupDisabled(t *testing.T) {
	store := &fakeStore{typ: "tv_show"}
	itemSvc := &fakeItems{}
	_, err := newService(&fakeClient{enabled: true}, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{
		Title: "Series", Metadata: map[string]any{"seasons": 2},
	}, CreateOptions{Lookup: boolPtr(false)})
	if err != nil || itemSvc.created == nil || itemSvc.created.Metadata["lookup_disabled"] != true || itemSvc.created.Metadata["seasons"] != 2 {
		t.Fatalf("err=%v input=%+v", err, itemSvc.created)
	}
}

func TestCreateMetadataOptOutSuppressesDefaultLookup(t *testing.T) {
	store := &fakeStore{typ: "movie"}
	itemSvc := &fakeItems{}
	client := &fakeClient{enabled: true, matches: []Suggestion{{SourceID: "7"}}}
	_, err := newService(client, store, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{
		Title: "Movie", Metadata: map[string]any{"lookup_disabled": true},
	}, CreateOptions{})
	if err != nil || itemSvc.created == nil || client.searchQuery != "" || store.applied != nil {
		t.Fatalf("err=%v created=%v search=%q applied=%v", err, itemSvc.created, client.searchQuery, store.applied)
	}
}

func TestCreateSelectedIDWithoutDetailsReturnsUpstreamUnavailable(t *testing.T) {
	itemSvc := &fakeItems{}
	client := &fakeClient{enabled: true}
	selectedID := 7
	_, err := newService(client, &fakeStore{typ: "movie"}, itemSvc).Create(t.Context(), uuid.New(), items.CreateInput{Title: "Movie"}, CreateOptions{TMDBID: &selectedID})
	var unavailable *apperr.UpstreamUnavailableError
	if !errors.As(err, &unavailable) || itemSvc.created != nil {
		t.Fatalf("err=%v created=%v", err, itemSvc.created)
	}
}

func TestCreateUsesFirstLocalizedMatchAndAtomicApply(t *testing.T) {
	client := &fakeClient{enabled: true, matches: []Suggestion{{SourceID: "7"}}, details: Details{
		SourceID: "7", Title: "Titolo locale", Fields: map[string]any{"release_year": 1999},
	}}
	store := &fakeStore{typ: "movie", result: items.Item{Title: "Titolo locale"}}
	itemSvc := &fakeItems{}
	in := items.CreateInput{Title: " titolo cercato ", Metadata: map[string]any{"director": "User"}}
	got, err := newService(client, store, itemSvc).Create(t.Context(), uuid.New(), in, CreateOptions{})
	if err != nil || got.Title != "Titolo locale" || store.applied == nil {
		t.Fatalf("got=%+v err=%v applied=%+v", got, err, store.applied)
	}
	if client.searchType != "movie" || client.searchQuery != in.Title || client.searchLanguage != "it-IT" {
		t.Fatalf("search = %q %q %q", client.searchType, client.searchQuery, client.searchLanguage)
	}
	p := store.applied
	if p.Mode != ModeCreate || p.Provider != ProviderTMDB || p.SourceID != "7" || p.Title != "Titolo locale" || p.OriginalTitle != in.Title || p.CreateInput.Metadata["director"] != "User" {
		t.Fatalf("apply = %+v", p)
	}
}

func TestApplyNoMatchRechecksVersion(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{ID: id, Title: "Unknown", Type: ptr("movie"), Version: 4}}
	store := &fakeStore{}
	got, err := newService(&fakeClient{enabled: true}, store, itemSvc).Apply(t.Context(), id, 4, ModeFill, nil)
	if err != nil || got.Version != 4 || itemSvc.updateCalls != 1 || store.applied != nil {
		t.Fatalf("got=%+v err=%v updates=%d applied=%v", got, err, itemSvc.updateCalls, store.applied)
	}
}

func TestApplyRejectsUnsupportedModeBeforeLookup(t *testing.T) {
	client := &fakeClient{enabled: true}
	_, err := newService(client, &fakeStore{}, &fakeItems{}).Apply(t.Context(), uuid.New(), 1, ModeCreate, nil)
	var validation *apperr.ValidationError
	if !errors.As(err, &validation) || client.searchQuery != "" {
		t.Fatalf("err=%v search=%q", err, client.searchQuery)
	}
}

func TestApplyPreservesExistingImageAndUsesStoredTMDBID(t *testing.T) {
	id := uuid.New()
	itemSvc := &fakeItems{item: items.Item{
		ID: id, Title: "Movie", Type: ptr("movie"), Version: 2,
		Image:    &items.Image{Source: items.ImageSourceURL, URL: "https://user.example/image.jpg"},
		Metadata: json.RawMessage(`{"tmdb_id":42}`),
	}}
	client := &fakeClient{enabled: true, details: Details{SourceID: "42", Title: "Localized", ImagePath: "/poster.jpg"}}
	store := &fakeStore{result: itemSvc.item}
	_, err := newService(client, store, itemSvc).Apply(t.Context(), id, 2, ModeRefresh, nil)
	if err != nil || store.applied == nil || store.applied.SourceID != "42" || store.applied.RefreshPoster || client.downloads != 0 {
		t.Fatalf("err=%v apply=%+v downloads=%d", err, store.applied, client.downloads)
	}
}

func TestApplyRejectsStaleSnapshotBeforeNetwork(t *testing.T) {
	itemSvc := &fakeItems{item: items.Item{Type: ptr("movie"), Version: 8}}
	client := &fakeClient{enabled: true}
	_, err := newService(client, &fakeStore{}, itemSvc).Apply(t.Context(), uuid.New(), 7, ModeFill, nil)
	var stale *apperr.VersionConflictError
	if !errors.As(err, &stale) || stale.Current != 8 || client.searchQuery != "" {
		t.Fatalf("err=%v search=%q", err, client.searchQuery)
	}
}

func boolPtr(v bool) *bool { return &v }

func ptr(s string) *string { return &s }
