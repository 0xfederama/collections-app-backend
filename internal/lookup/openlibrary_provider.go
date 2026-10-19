package lookup

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/openlibrary"
)

const (
	maxAuthorRunes = 100 // the book `author` field is a text field of at most 100 characters
	maxTitleRunes  = 300 // item titles are 1 to 300 characters
)

// openLibraryProvider adapts the Open Library client to Provider for book lists
// (F-016, D-095). It declares no retention. Its cover id is the Details.ImagePath.
type openLibraryProvider struct{ client *openlibrary.Client }

// NewOpenLibraryProvider wraps an Open Library client.
func NewOpenLibraryProvider(client *openlibrary.Client) Provider {
	return openLibraryProvider{client: client}
}

func (openLibraryProvider) Name() string { return ProviderOpenLibrary }

func (p openLibraryProvider) Enabled() bool { return p.client.Enabled() }

func (openLibraryProvider) ValidateSourceID(id string) error {
	return ValidSourceID(ProviderOpenLibrary, id)
}

func (p openLibraryProvider) Search(ctx context.Context, typ, query, language string) ([]Suggestion, error) {
	if typ != types.Book {
		return nil, openlibrary.ErrInvalid
	}
	matches, err := p.client.Search(ctx, query, language)
	if err != nil {
		return nil, openLibraryError(err)
	}
	out := make([]Suggestion, 0, len(matches))
	for _, m := range matches {
		out = append(out, Suggestion{
			SourceID: m.WorkID, Title: m.Title, Year: m.Year, Pages: m.Pages, ISBN: m.ISBN,
			Authors: strings.Join(m.Authors, ", "), ImageURL: p.client.CoverURL(m.CoverID),
		})
	}
	return out, nil
}

// Details returns the work's fields: author (authors joined with ", "), pages,
// first_publish_year and isbn, plus the Open Library link, proposed for `stores`
// as a fill-only value (it never replaces or removes a user's store choices).
func (p openLibraryProvider) Details(ctx context.Context, typ, sourceID, language, _ string) (Details, error) {
	if typ != types.Book {
		return Details{}, openlibrary.ErrInvalid
	}
	m, err := p.client.Work(ctx, sourceID, language)
	if err != nil {
		return Details{}, openLibraryError(err)
	}
	fields := map[string]any{}
	if author := joinAuthors(m.Authors); author != "" {
		fields["author"] = author
	}
	if m.Pages > 0 {
		fields["pages"] = m.Pages
	}
	if m.Year > 0 {
		fields["first_publish_year"] = m.Year
	}
	if m.ISBN != "" {
		fields["isbn"] = m.ISBN
	}
	d := Details{
		SourceID: m.WorkID, Title: truncateRunes(m.Title, maxTitleRunes), Fields: fields,
		FillOnly: map[string]any{"stores": []string{"openlibrary"}},
	}
	if m.CoverID > 0 {
		d.ImagePath = strconv.Itoa(m.CoverID)
	}
	return d, nil
}

func (p openLibraryProvider) DownloadImage(ctx context.Context, imagePath string) ([]byte, error) {
	id, err := strconv.Atoi(imagePath)
	if err != nil {
		return nil, openlibrary.ErrInvalid
	}
	b, err := p.client.DownloadCover(ctx, id)
	return b, openLibraryError(err)
}

func openLibraryError(err error) error {
	switch {
	case errors.Is(err, openlibrary.ErrDisabled):
		return ErrDisabled
	case errors.Is(err, openlibrary.ErrNoCover):
		return ErrNoImage
	}
	return err
}

// joinAuthors joins author names with ", " whole, as many as fit the field's
// limit; a single over-long name is cut.
func joinAuthors(authors []string) string {
	var out string
	for _, a := range authors {
		next := a
		if out != "" {
			next = out + ", " + a
		}
		if utf8.RuneCountInString(next) > maxAuthorRunes {
			if out == "" {
				return truncateRunes(a, maxAuthorRunes)
			}
			break
		}
		out = next
	}
	return out
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n]))
}
