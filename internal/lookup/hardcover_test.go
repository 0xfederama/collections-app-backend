package lookup

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/hardcover"
	"github.com/0xfederama/collections-app-backend/internal/items"
)

type fakeHardcover struct {
	enabled     bool
	isbnBook    hardcover.Book
	isbnOK      bool
	titleBooks  []hardcover.Book
	err         error
	coverErr    error
	cover       []byte
	isbnCalls   []string
	titleCalls  []string
	coverCalls  []string
	anyCallMade bool
}

func (f *fakeHardcover) Enabled() bool { return f.enabled }
func (f *fakeHardcover) FindByISBN(_ context.Context, isbn string) (hardcover.Book, bool, error) {
	f.anyCallMade = true
	f.isbnCalls = append(f.isbnCalls, isbn)
	return f.isbnBook, f.isbnOK, f.err
}
func (f *fakeHardcover) SearchTitle(_ context.Context, title string) ([]hardcover.Book, error) {
	f.anyCallMade = true
	f.titleCalls = append(f.titleCalls, title)
	return f.titleBooks, f.err
}
func (f *fakeHardcover) DownloadCover(_ context.Context, u string) ([]byte, error) {
	f.anyCallMade = true
	f.coverCalls = append(f.coverCalls, u)
	return f.cover, f.coverErr
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 6))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// olWith is an Open Library fake whose chosen work has the given fields (no cover).
func olWith(fields map[string]any) *fakeClient {
	book := bookClient(true)
	book.details.ImagePath = ""
	book.details.Fields = fields
	return book
}

func createBook(t *testing.T, svc *Service, title string, opts CreateOptions) (items.Item, error) {
	t.Helper()
	return svc.Create(t.Context(), uuid.New(), items.CreateInput{Title: title}, opts)
}

func TestHardcover_GapFillIsFillOnly(t *testing.T) {
	hc := hardcover.Book{Authors: []string{"HC Author", "Second"}, Pages: 321}
	for _, tc := range []struct {
		name       string
		ol         map[string]any
		wantAuthor any
		wantPages  any
		wantCalls  int
	}{
		{"author only missing", map[string]any{"pages": 608, "isbn": "9780441172719"}, "HC Author, Second", 608, 1},
		{"pages only missing", map[string]any{"author": "Frank Herbert", "isbn": "9780441172719"}, "Frank Herbert", 321, 1},
		{"both missing", map[string]any{"isbn": "9780441172719"}, "HC Author, Second", 321, 1},
		{"nothing missing", map[string]any{"author": "Frank Herbert", "pages": 608, "isbn": "9780441172719"}, "Frank Herbert", 608, 0},
		{"no isbn", map[string]any{"pages": 608}, nil, 608, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book, h := olWith(tc.ol), &fakeHardcover{enabled: true, isbnBook: hc, isbnOK: true}
			store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
			svc := bookService(book, &fakeClient{}, store, &fakeItems{}).WithHardcover(h)
			if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			p := store.applied
			if p == nil || p.Mode != ModeCreate || p.Provider != ProviderOpenLibrary || p.SourceID != "OL1W" {
				t.Fatalf("apply = %+v", p)
			}
			if p.Fields["author"] != tc.wantAuthor || p.Fields["pages"] != tc.wantPages || len(h.isbnCalls) != tc.wantCalls {
				t.Errorf("fields = %v calls = %v", p.Fields, h.isbnCalls)
			}
			if tc.wantCalls == 1 && h.isbnCalls[0] != "9780441172719" {
				t.Errorf("isbn searched = %q", h.isbnCalls[0])
			}
			if len(h.titleCalls) != 0 || len(h.coverCalls) != 0 {
				t.Errorf("only the ISBN search is allowed: %+v", h)
			}
		})
	}
}

func TestHardcover_GapFillRejectedOrFailedLeavesOpenLibraryUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *fakeHardcover
	}{
		{"isbn not in hit", &fakeHardcover{enabled: true, isbnOK: false, isbnBook: hardcover.Book{Authors: []string{"X"}, Pages: 1}}},
		{"401", &fakeHardcover{enabled: true, err: errors.New("hardcover request failed (HTTP 401)")}},
		{"timeout", &fakeHardcover{enabled: true, err: context.DeadlineExceeded}},
		{"disabled", &fakeHardcover{enabled: false, isbnOK: true, isbnBook: hardcover.Book{Authors: []string{"X"}, Pages: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := olWith(map[string]any{"isbn": "9780441172719"})
			store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
			svc := bookService(book, &fakeClient{}, store, &fakeItems{}).WithHardcover(tc.h)
			if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			p := store.applied
			if p == nil || p.Mode != ModeCreate || len(p.Fields) != 1 || p.Fields["isbn"] != "9780441172719" {
				t.Fatalf("apply = %+v", p)
			}
			if !tc.h.enabled && tc.h.anyCallMade {
				t.Error("a disabled client must never be called")
			}
		})
	}
}

func TestHardcover_GapFillNotUsedForSelectedOrExplicitRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts CreateOptions
	}{
		{"source_id", CreateOptions{SourceID: ptr("OL1W")}},
		{"lookup true", CreateOptions{Lookup: boolPtr(true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := olWith(map[string]any{"isbn": "9780441172719"})
			h := &fakeHardcover{enabled: true, isbnOK: true, isbnBook: hardcover.Book{Authors: []string{"X"}, Pages: 1}}
			store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
			svc := bookService(book, &fakeClient{}, store, &fakeItems{}).WithHardcover(h)
			if _, err := createBook(t, svc, "Dune", tc.opts); err != nil {
				t.Fatal(err)
			}
			if tc.name == "source_id" && h.anyCallMade {
				t.Errorf("hardcover called for a selected id")
			}
			if store.applied.Fields["author"] != nil {
				t.Errorf("fields = %v", store.applied.Fields)
			}
		})
	}
}

func hcDune() hardcover.Book {
	return hardcover.Book{
		Title: "Dune", Authors: []string{"Frank Herbert"}, Pages: 412,
		ISBNs: []string{"bad", "9780441172719"}, ImageURL: "https://assets.hardcover.app/dune.png",
	}
}

func TestHardcover_OpenLibraryEmptyExactTitleIsAccepted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typed string
		doc   hardcover.Book
	}{
		{"title", " dune! ", hcDune()},
		{"alternative title", "Dune Messiah Saga", func() hardcover.Book {
			b := hcDune()
			b.Title = "Something else"
			b.AlternativeTitles = []string{"Dune Messiah saga"}
			return b
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := bookClient(true)
			book.matches = nil
			h := &fakeHardcover{enabled: true, titleBooks: []hardcover.Book{tc.doc}, cover: tinyPNG(t)}
			store := &fakeStore{typ: "book", result: items.Item{Title: tc.typed}}
			itemSvc := &fakeItems{}
			svc := bookService(book, &fakeClient{}, store, itemSvc).WithHardcover(h)
			if _, err := createBook(t, svc, tc.typed, CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			p := store.applied
			if p == nil || p.Mode != ModeCreateUntracked || itemSvc.created != nil {
				t.Fatalf("apply = %+v created=%v", p, itemSvc.created)
			}
			if p.Title != tc.typed || p.OriginalTitle != tc.typed || p.Provider != "" || p.SourceID != "" || p.Type != "book" {
				t.Errorf("title/provider = %+v", p)
			}
			if p.Fields["author"] != "Frank Herbert" || p.Fields["pages"] != 412 || p.Fields["isbn"] != "9780441172719" || len(p.Fields) != 3 {
				t.Errorf("fields = %v", p.Fields)
			}
			if _, ok := p.Fields["openlibrary_id"]; ok {
				t.Error("no openlibrary_id for a Hardcover-only match")
			}
			if p.Poster == nil || len(h.coverCalls) != 1 || h.coverCalls[0] != tc.doc.ImageURL {
				t.Errorf("cover: poster=%v calls=%v", p.Poster, h.coverCalls)
			}
			if got, want := p.FillOnly["stores"], []string{"kobo"}; !equalStrings(got, want) {
				t.Errorf("default store fill = %v, want %v", got, want)
			}
			if h.titleCalls[0] != tc.typed || len(h.isbnCalls) != 0 {
				t.Errorf("calls = %+v", h)
			}
		})
	}
}

func TestHardcover_OpenLibraryFailureFallsBackToHardcover(t *testing.T) {
	book := bookClient(true)
	book.err = errors.New("down")
	h := &fakeHardcover{enabled: true, titleBooks: []hardcover.Book{hcDune()}}
	store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
	svc := bookService(book, &fakeClient{}, store, &fakeItems{}).WithHardcover(h)
	if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if store.applied == nil || store.applied.Mode != ModeCreateUntracked || store.applied.Poster != nil {
		t.Fatalf("apply = %+v (no cover bytes: still applied without image)", store.applied)
	}
}

func TestHardcover_NoExactMatchOrFailureIsAPlainCreate(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *fakeHardcover
	}{
		{"non matching title", &fakeHardcover{enabled: true, titleBooks: []hardcover.Book{{Title: "Dune Messiah", AlternativeTitles: []string{"Children"}, Pages: 1}}}},
		{"no hits", &fakeHardcover{enabled: true}},
		{"401", &fakeHardcover{enabled: true, err: errors.New("hardcover request failed (HTTP 401)")}},
		{"disabled", &fakeHardcover{enabled: false, titleBooks: []hardcover.Book{hcDune()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := bookClient(true)
			book.matches = nil
			store := &fakeStore{typ: "book"}
			itemSvc := &fakeItems{}
			svc := bookService(book, &fakeClient{}, store, itemSvc).WithHardcover(tc.h)
			if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if store.applied != nil || itemSvc.created == nil || itemSvc.created.Title != "Dune" {
				t.Fatalf("applied=%+v created=%+v", store.applied, itemSvc.created)
			}
			if !tc.h.enabled && tc.h.anyCallMade {
				t.Error("a disabled client must never be called")
			}
		})
	}
}

func TestHardcover_CoverFailureIsNonFatal(t *testing.T) {
	for name, h := range map[string]*fakeHardcover{
		"download error": {enabled: true, titleBooks: []hardcover.Book{hcDune()}, coverErr: errors.New("boom")},
		"not an image":   {enabled: true, titleBooks: []hardcover.Book{hcDune()}, cover: []byte("nope")},
	} {
		t.Run(name, func(t *testing.T) {
			book := bookClient(true)
			book.matches = nil
			store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
			svc := bookService(book, &fakeClient{}, store, &fakeItems{}).WithHardcover(h)
			if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
				t.Fatal(err)
			}
			if store.applied == nil || store.applied.Mode != ModeCreateUntracked || store.applied.Poster != nil || store.applied.Fields["pages"] != 412 {
				t.Fatalf("apply = %+v", store.applied)
			}
		})
	}
}

func TestHardcover_ExplicitLookupKeepsOpenLibraryOnlyBehaviour(t *testing.T) {
	h := &fakeHardcover{enabled: true, titleBooks: []hardcover.Book{hcDune()}, cover: tinyPNG(t)}
	// lookup:true with no Open Library result: plain create, Hardcover untouched.
	book := bookClient(true)
	book.matches = nil
	store := &fakeStore{typ: "book"}
	itemSvc := &fakeItems{}
	svc := bookService(book, &fakeClient{}, store, itemSvc).WithHardcover(h)
	if _, err := createBook(t, svc, "Dune", CreateOptions{Lookup: boolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	if store.applied != nil || itemSvc.created == nil || h.anyCallMade {
		t.Fatalf("applied=%+v created=%v hardcover called=%v", store.applied, itemSvc.created, h.anyCallMade)
	}
	// lookup:true with an upstream failure: still the 503 error.
	failing := bookClient(true)
	failing.err = errors.New("down")
	svc = bookService(failing, &fakeClient{}, &fakeStore{typ: "book"}, &fakeItems{}).WithHardcover(h)
	if _, err := createBook(t, svc, "Dune", CreateOptions{Lookup: boolPtr(true)}); err == nil || h.anyCallMade {
		t.Fatalf("err=%v hardcover called=%v", err, h.anyCallMade)
	}
	// source_id with no details: UpstreamUnavailable, no Hardcover.
	if _, err := createBook(t, svc, "Dune", CreateOptions{SourceID: ptr("OL1W")}); err == nil || h.anyCallMade {
		t.Fatalf("err=%v hardcover called=%v", err, h.anyCallMade)
	}
}

func TestHardcover_NotUsedOutsideBooks(t *testing.T) {
	h := &fakeHardcover{enabled: true, titleBooks: []hardcover.Book{hcDune()}}
	tm := &fakeClient{enabled: true}
	store := &fakeStore{typ: "movie"}
	itemSvc := &fakeItems{}
	svc := bookService(bookClient(true), tm, store, itemSvc).WithHardcover(h)
	if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if h.anyCallMade || store.applied != nil {
		t.Fatalf("hardcover called=%v applied=%+v", h.anyCallMade, store.applied)
	}
}

func TestHardcover_ApplyWithoutSelectionGapFillsButNeverCreatesUntracked(t *testing.T) {
	book := olWith(map[string]any{"isbn": "9780441172719"})
	h := &fakeHardcover{enabled: true, isbnOK: true, isbnBook: hardcover.Book{Authors: []string{"HC"}, Pages: 5}}
	store := &fakeStore{typ: "book", result: items.Item{Title: "Dune"}}
	typ := "book"
	itemSvc := &fakeItems{item: items.Item{ID: uuid.New(), Title: "Dune", Version: 1, Type: &typ}}
	svc := bookService(book, &fakeClient{}, store, itemSvc).WithHardcover(h)
	if _, err := svc.Apply(t.Context(), itemSvc.item.ID, 1, ModeFill, Selection{}); err != nil {
		t.Fatal(err)
	}
	if store.applied == nil || store.applied.Mode != ModeFill || store.applied.Fields["author"] != "HC" || store.applied.Fields["pages"] != 5 {
		t.Fatalf("apply = %+v", store.applied)
	}
	// A selected id stays Open Library only.
	h.isbnCalls = nil
	if _, err := svc.Apply(t.Context(), itemSvc.item.ID, 1, ModeFill, Selection{SourceID: ptr("OL1W")}); err != nil || len(h.isbnCalls) != 0 {
		t.Fatalf("err=%v isbn calls=%v", err, h.isbnCalls)
	}
}

func TestHardcover_PickHardcoverPrefersMostCompleteThenEarliest(t *testing.T) {
	books := []hardcover.Book{
		{Title: "Other", Authors: []string{"x"}, Pages: 1},
		{Title: "Dune"},
		{Title: "Dune", Authors: []string{"A"}},
		{Title: "dune", Authors: []string{"B"}, Pages: 9},
		{Title: "Dune", Authors: []string{"C"}, Pages: 9},
	}
	got, ok := pickHardcover("DUNE", books)
	if !ok || got.Authors[0] != "B" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
	if _, ok := pickHardcover("Dun", books); ok {
		t.Error("partial titles must not match")
	}
	if _, ok := pickHardcover("  !! ", books); ok {
		t.Error("blank normalised title must not match")
	}
}

// captureLogs routes the default slog logger into a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestHardcover_TokenNeverAppearsInLogsOrErrors(t *testing.T) {
	const token = "tok-SECRET-xyz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	t.Cleanup(srv.Close)
	client := hardcover.NewWithOptions(hardcover.Options{Token: "Bearer " + token, BaseURL: srv.URL, HTTPClient: srv.Client(), RequestInterval: time.Millisecond})
	logs := captureLogs(t)

	book := bookClient(true)
	book.matches = nil
	store := &fakeStore{typ: "book"}
	itemSvc := &fakeItems{}
	svc := bookService(book, &fakeClient{}, store, itemSvc).WithHardcover(client)
	if _, err := createBook(t, svc, "Dune", CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if itemSvc.created == nil {
		t.Fatal("a Hardcover 401 must degrade to a plain create")
	}
	if !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), token) {
		t.Errorf("logs = %q", logs.String())
	}
	_, err := client.SearchTitle(t.Context(), "Dune")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("err = %v", err)
	}
	// A cancelled request is logged at DEBUG, not WARN.
	logs.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	logHardcover(ctx, "x", context.Canceled)
	if strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "level=DEBUG") {
		t.Errorf("cancel log = %q", logs.String())
	}
}
