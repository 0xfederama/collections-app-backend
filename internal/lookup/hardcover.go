package lookup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/hardcover"
	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/workspace"
)

// BookGapFiller is the optional Hardcover source for the automatic, best-effort
// book lookup. Open Library stays primary; this only fills what it lacks. Every
// failure of it is swallowed by the callers.
type BookGapFiller interface {
	Enabled() bool
	FindByISBN(ctx context.Context, isbn string) (hardcover.Book, bool, error)
	SearchTitle(ctx context.Context, title string) ([]hardcover.Book, error)
	DownloadCover(ctx context.Context, imageURL string) ([]byte, error)
}

func (s *Service) hardcoverEnabled() bool {
	return s.hardcover != nil && s.hardcover.Enabled()
}

// logHardcover logs a swallowed Hardcover failure: DEBUG when the request was
// cancelled, WARN otherwise. Errors from the client never contain the token.
func logHardcover(ctx context.Context, msg string, err error) {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		slog.DebugContext(ctx, msg, "err", err)
		return
	}
	slog.WarnContext(ctx, msg, "err", err)
}

// fillGaps asks Hardcover, by the work's ISBN, for the author and page count the
// Open Library work lacks. Fill-only: an existing value is never overwritten, the
// title and cover are never taken. A hit counts only if its ISBN list contains the
// searched ISBN (checked by the client). Any failure leaves details unchanged.
func (s *Service) fillGaps(ctx context.Context, d Details) Details {
	if !s.hardcoverEnabled() {
		return d
	}
	_, hasAuthor := d.Fields["author"]
	_, hasPages := d.Fields["pages"]
	isbn, _ := d.Fields["isbn"].(string)
	if (hasAuthor && hasPages) || isbn == "" {
		return d
	}
	book, ok, err := s.hardcover.FindByISBN(ctx, isbn)
	if err != nil {
		logHardcover(ctx, "hardcover gap fill failed; keeping the Open Library result", err)
		return d
	}
	if !ok {
		return d
	}
	fields := make(map[string]any, len(d.Fields)+2)
	for k, v := range d.Fields {
		fields[k] = v
	}
	if author := joinAuthors(book.Authors); !hasAuthor && author != "" {
		fields["author"] = author
	}
	if !hasPages && book.Pages > 0 {
		fields["pages"] = book.Pages
	}
	d.Fields = fields
	return d
}

// pickHardcover chooses, among the documents whose normalised title (or one of
// their alternative titles) equals the normalised typed title, the one with the
// most of author and pages, ties to the earliest. ok is false without an exact
// title match.
func pickHardcover(title string, books []hardcover.Book) (hardcover.Book, bool) {
	want := normalizeTitle(title)
	if want == "" {
		return hardcover.Book{}, false
	}
	best, bestScore := -1, -1
	for i, b := range books {
		match := normalizeTitle(b.Title) == want
		for _, alt := range b.AlternativeTitles {
			match = match || normalizeTitle(alt) == want
		}
		if !match {
			continue
		}
		score := 0
		if joinAuthors(b.Authors) != "" {
			score++
		}
		if b.Pages > 0 {
			score++
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return hardcover.Book{}, false
	}
	return books[best], true
}

// createFromHardcover is the automatic book create when Open Library has nothing
// (no results, or a non-cancel failure). It searches Hardcover by the typed title
// and accepts only an exact normalised title match. The item keeps the typed title
// and gets author, pages, the first ISBN and the cover; no lookup_imports row and
// no openlibrary_id are written (ModeCreateUntracked). ok is false whenever the
// caller should do a plain create instead.
func (s *Service) createFromHardcover(ctx context.Context, provider Provider, listID uuid.UUID, typ string, in items.CreateInput, settings workspace.Settings) (items.Item, bool) {
	if typ != types.Book || provider.Name() != ProviderOpenLibrary || !s.hardcoverEnabled() || ctx.Err() != nil {
		return items.Item{}, false
	}
	books, err := s.hardcover.SearchTitle(ctx, in.Title)
	if err != nil {
		logHardcover(ctx, "hardcover title lookup failed; creating without lookup", err)
		return items.Item{}, false
	}
	book, ok := pickHardcover(in.Title, books)
	if !ok {
		return items.Item{}, false
	}
	fields := map[string]any{}
	if author := joinAuthors(book.Authors); author != "" {
		fields["author"] = author
	}
	if book.Pages > 0 {
		fields["pages"] = book.Pages
	}
	if isbn := book.FirstISBN(); isbn != "" {
		fields["isbn"] = isbn
	}
	poster := s.hardcoverCover(ctx, book.ImageURL)
	it, err := s.store.Apply(ctx, ApplyParams{
		Mode: ModeCreateUntracked, ListID: listID, Type: typ, CreateInput: &in,
		Title: in.Title, OriginalTitle: in.Title,
		Fields: fields, FillOnly: defaultStoreFill(provider, nil, settings.PreferredStore),
		Poster: poster,
	})
	if err != nil {
		logHardcover(ctx, "hardcover create failed; creating without lookup", err)
		return items.Item{}, false
	}
	return it, true
}

// hardcoverCover downloads and processes the cover; any failure is non-fatal (nil).
func (s *Service) hardcoverCover(ctx context.Context, imageURL string) *images.Processed {
	if imageURL == "" {
		return nil
	}
	raw, err := s.hardcover.DownloadCover(ctx, imageURL)
	if err != nil {
		logHardcover(ctx, "hardcover cover download failed; importing without an image", err)
		return nil
	}
	processed, err := images.Process(bytes.NewReader(raw))
	if err != nil {
		slog.WarnContext(ctx, "hardcover cover is not a usable image; importing without one", "err", err)
		return nil
	}
	return processed
}
