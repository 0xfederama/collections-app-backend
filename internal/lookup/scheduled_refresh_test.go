package lookup

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

// movieForRefresh is a movie item with the given metadata and image, as the service reads it.
func movieForRefresh(id uuid.UUID, version int, metadata string, image *items.Image) *fakeItems {
	return &fakeItems{item: items.Item{ID: id, Title: "Matrix", Type: ptr("movie"), Version: version, Image: image, Metadata: json.RawMessage(metadata)}}
}

func uploadedImage(id uuid.UUID) *items.Image {
	return &items.Image{Source: items.ImageSourceUpload, AttachmentID: id, URL: "/uploads/x.jpg"}
}

// The weekly refresh of one movie or TV item (D-123), with the service's fakes.

func TestRefreshScheduled_OverwritesFetchedFieldsAndTitle(t *testing.T) {
	id := uuid.New()
	itemSvc := movieForRefresh(id, 2, `{"tmdb_id":603,"runtime_minutes":140}`, nil)
	client := &fakeClient{enabled: true, details: Details{SourceID: "603", Title: "The Matrix", Fields: map[string]any{"release_year": 1999, "runtime_minutes": 136}}}
	store := &fakeStore{result: itemSvc.item}
	if err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 2); err != nil {
		t.Fatal(err)
	}
	p := store.applied
	if p == nil || p.Mode != ModeScheduled || p.Provider != ProviderTMDB || p.SourceID != "603" || p.Type != "movie" ||
		p.Title != "The Matrix" || p.OriginalTitle != "Matrix" || p.ExpectedVersion != 2 || p.Fields["runtime_minutes"] != 136 {
		t.Fatalf("apply = %+v", p)
	}
	if client.detailsID != "603" {
		t.Errorf("details fetched for %q, want the stored tmdb_id", client.detailsID)
	}
}

func TestRefreshScheduled_ProviderFailuresWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error // nil: any error
	}{
		{name: "TMDB rate limit", err: ErrProviderRateLimited, want: ErrProviderRateLimited},
		{name: "TMDB 404 for the id", err: ErrMatchNotFound, want: ErrMatchNotFound},
		{name: "TMDB unreachable or 5xx", err: errors.New("TMDB request failed")},
		{name: "TMDB disabled", err: ErrDisabled, want: ErrDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			itemSvc := movieForRefresh(id, 1, `{"tmdb_id":603,"director":"Mine"}`, nil)
			store := &fakeStore{}
			err := newService(&fakeClient{enabled: true, err: tc.err}, store, itemSvc).RefreshScheduled(t.Context(), id, 1)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || store.applied != nil {
				t.Fatalf("err=%v applied=%v, want %v and no write", err, store.applied, tc.want)
			}
		})
	}
}

func TestRefreshScheduled_PosterFailuresWriteNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		image []byte
		err   error
		want  error
	}{
		{name: "poster rate limit", err: ErrProviderRateLimited, want: ErrProviderRateLimited},
		{name: "poster is not an image", image: []byte("not an image")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			itemSvc := movieForRefresh(id, 1, `{"tmdb_id":603}`, nil)
			client := &fakeClient{enabled: true, details: Details{SourceID: "603", Title: "The Matrix", ImagePath: "/p.jpg"}, downloadErr: tc.err, image: tc.image}
			store := &fakeStore{}
			err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 1)
			var upstream *apperr.UpstreamUnavailableError
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || (tc.want == nil && !errors.As(err, &upstream)) || store.applied != nil {
				t.Fatalf("err=%v applied=%v, want %v and no write", err, store.applied, tc.want)
			}
		})
	}
}

func TestRefreshScheduled_SkipsWhatItMustNotRefresh(t *testing.T) {
	id := uuid.New()
	t.Run("books are never refreshed", func(t *testing.T) {
		book := bookClient(true)
		itemSvc := &fakeItems{item: items.Item{ID: id, Title: "Dune", Type: ptr("book"), Version: 1, Metadata: json.RawMessage(`{"openlibrary_id":"OL1W"}`)}}
		store := &fakeStore{}
		err := bookService(book, &fakeClient{enabled: true}, store, itemSvc).RefreshScheduled(t.Context(), id, 1)
		if !errors.Is(err, errNotScheduled) || book.detailsID != "" || store.applied != nil {
			t.Fatalf("err=%v details=%q applied=%v", err, book.detailsID, store.applied)
		}
	})
	t.Run("lookup_disabled items keep their values", func(t *testing.T) {
		client := &fakeClient{enabled: true}
		itemSvc := movieForRefresh(id, 1, `{"tmdb_id":603,"lookup_disabled":true}`, nil)
		store := &fakeStore{}
		err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 1)
		if !errors.Is(err, errNotScheduled) || client.detailsID != "" || store.applied != nil {
			t.Fatalf("err=%v details=%q applied=%v", err, client.detailsID, store.applied)
		}
	})
	t.Run("a version moved on is a conflict", func(t *testing.T) {
		client := &fakeClient{enabled: true}
		itemSvc := movieForRefresh(id, 4, `{"tmdb_id":603}`, nil)
		store := &fakeStore{}
		err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 3)
		var conflict *apperr.VersionConflictError
		if !errors.As(err, &conflict) || client.detailsID != "" || store.applied != nil {
			t.Fatalf("err=%v details=%q applied=%v", err, client.detailsID, store.applied)
		}
	})
	t.Run("no valid TMDB id, nothing fetched", func(t *testing.T) {
		client := &fakeClient{enabled: true}
		itemSvc := movieForRefresh(id, 1, `{"release_year":1999}`, nil)
		store := &fakeStore{}
		if err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 1); err == nil || client.detailsID != "" || store.applied != nil {
			t.Fatalf("err=%v details=%q applied=%v", err, client.detailsID, store.applied)
		}
	})
	t.Run("lookup disabled on the server", func(t *testing.T) {
		client := &fakeClient{enabled: false}
		itemSvc := movieForRefresh(id, 1, `{"tmdb_id":603}`, nil)
		var disabled *apperr.LookupDisabledError
		if err := newService(client, &fakeStore{}, itemSvc).RefreshScheduled(t.Context(), id, 1); !errors.As(err, &disabled) {
			t.Fatalf("err=%v, want lookup disabled", err)
		}
	})
}

// The poster rule: a user's image is never fetched or replaced, a poster the
// import fetched follows TMDB's path, and a missing image is filled once.
func TestRefreshScheduled_PosterFollowsOwnershipAndPath(t *testing.T) {
	owned := uuid.New()
	for _, tc := range []struct {
		name        string
		image       *items.Image
		record      Record
		found       bool
		path        string
		wantFetch   bool
		wantReplace bool // RefreshPoster on the apply
	}{
		{name: "owned poster, same TMDB path", image: uploadedImage(owned), record: Record{AttachmentID: &owned, PosterPath: "/p.jpg"}, found: true, path: "/p.jpg"},
		{name: "owned poster, TMDB path changed", image: uploadedImage(owned), record: Record{AttachmentID: &owned, PosterPath: "/old.jpg"}, found: true, path: "/new.jpg", wantFetch: true, wantReplace: true},
		{name: "owned poster, TMDB has none now", image: uploadedImage(owned), record: Record{AttachmentID: &owned, PosterPath: "/old.jpg"}, found: true, path: "", wantReplace: true},
		{name: "user URL image", image: &items.Image{Source: items.ImageSourceURL, URL: "https://user.example/x.jpg"}, record: Record{PosterPath: "/old.jpg"}, found: true, path: "/new.jpg"},
		{name: "user replaced the owned poster", image: uploadedImage(uuid.New()), record: Record{AttachmentID: &owned, PosterPath: "/old.jpg"}, found: true, path: "/new.jpg"},
		{name: "no image, never fetched", path: "/p.jpg", wantFetch: true, wantReplace: true},
		{name: "no image, user removed the poster", record: Record{AttachmentID: &owned, PosterPath: "/p.jpg"}, found: true, path: "/p.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			itemSvc := movieForRefresh(id, 1, `{"tmdb_id":603}`, tc.image)
			client := &fakeClient{enabled: true, details: Details{SourceID: "603", Title: "The Matrix", ImagePath: tc.path}, image: tinyPNG(t)}
			store := &fakeStore{found: tc.found, record: tc.record, result: itemSvc.item}
			if err := newService(client, store, itemSvc).RefreshScheduled(t.Context(), id, 1); err != nil {
				t.Fatal(err)
			}
			if (client.downloads == 1) != tc.wantFetch {
				t.Errorf("poster downloads = %d, want fetch %v", client.downloads, tc.wantFetch)
			}
			if store.applied == nil || store.applied.RefreshPoster != tc.wantReplace {
				t.Errorf("apply = %+v, want RefreshPoster %v", store.applied, tc.wantReplace)
			}
		})
	}
}
