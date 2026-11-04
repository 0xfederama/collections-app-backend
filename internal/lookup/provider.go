package lookup

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
)

// Provider names are stored in lookup_imports.provider.
const (
	ProviderTMDB        = "tmdb"
	ProviderOpenLibrary = "openlibrary"
)

// ErrDisabled is returned by a Provider that is not configured. It is the one
// sentinel every adapter maps its client's "disabled" error to, so provider errors
// are mapped to API errors in one place (ProviderError).
var ErrDisabled = errors.New("lookup is disabled")

// ErrNoImage is returned by DownloadImage when the provider has no image for the
// match (a book without a cover). The match is still applied, without an image.
var ErrNoImage = errors.New("lookup has no image")

// Suggestion is one provider-ranked search result. Fields a provider does not
// have stay zero: Overview and OriginalTitle are TMDB's; Authors, Pages and ISBN
// belong to books.
type Suggestion struct {
	SourceID      string
	Title         string
	OriginalTitle string
	Year          int
	ImageURL      string
	Overview      string
	Authors       string
	Pages         int
	ISBN          string
}

// Details is the normalized result of a provider's detail fetch.
type Details struct {
	SourceID string
	Title    string
	// Fields is the provider metadata object, replaced by rematch and the weekly refresh and
	// tracked as imported values.
	Fields map[string]any
	// FillOnly values are applied only on create and fill, only where the item has
	// no value yet (absent, or an empty list on fill), and are never replaced,
	// removed or tracked as imported: they become the user's own (D-096: the
	// Open Library link in `stores`).
	FillOnly map[string]any
	// ImagePath is the provider's opaque handle for DownloadImage ("" = no image).
	ImagePath string
}

// Provider is one metadata source (TMDB, Open Library). Implementations wrap a
// bounded HTTP client and map its disabled state to ErrDisabled.
type Provider interface {
	// Name is the lookup_imports.provider value.
	Name() string
	Enabled() bool
	// ValidateSourceID rejects a malformed id before any URL is built; the error
	// is a validation error naming the request field.
	ValidateSourceID(id string) error
	Search(ctx context.Context, typ, query, language string) ([]Suggestion, error)
	Details(ctx context.Context, typ, sourceID, language, country string) (Details, error)
	DownloadImage(ctx context.Context, imagePath string) ([]byte, error)
}

// Providers is the registry: the provider serving each item type. A type with
// no entry has no lookup.
type Providers map[string]Provider

// legacyDeadlines are the provenance deadlines of a TMDB row (D-089). Since D-123
// nothing acts on them: movies and TV are refreshed weekly and never expire. They
// are still written because lookup_imports_retention_check requires them, and an
// older release on the same database (a rollback) still reads them. Open Library
// rows have none (D-095).
type legacyDeadlines struct{ RefreshMonths, ExpireMonths int }

var tmdbLegacyDeadlines = &legacyDeadlines{RefreshMonths: 5, ExpireMonths: 6}

// legacyDeadlinesFor returns the deadlines written for a provider, nil when it has none.
func legacyDeadlinesFor(provider string) *legacyDeadlines {
	if provider == ProviderTMDB {
		return tmdbLegacyDeadlines
	}
	return nil
}

// ErrProviderRateLimited is returned by a Provider the provider answers 429 (TMDB).
// The weekly pass stops its batch on it; manual lookup maps it to upstream_unavailable.
var ErrProviderRateLimited = errors.New("lookup provider rate limited the request")

// ErrMatchNotFound is returned by a Provider when the id no longer exists upstream
// (TMDB 404). The weekly pass leaves the item untouched and retries it next week.
var ErrMatchNotFound = errors.New("lookup match not found")

// providerTypes are the item types a provider may serve.
var providerTypes = map[string][]string{
	ProviderTMDB:        {types.Movie, types.TVShow},
	ProviderOpenLibrary: {types.Book},
}

// sourceIDField is the request field naming the source id, for validation errors.
func sourceIDField(provider string) string {
	if provider == ProviderTMDB {
		return "tmdb_id"
	}
	return "source_id"
}

// providerLabel is the provider's display name in messages.
func providerLabel(provider string) string {
	if provider == ProviderTMDB {
		return "TMDB"
	}
	return "Open Library"
}

var openLibraryWorkRe = regexp.MustCompile(`^OL[0-9]+W$`)

// ValidSourceID checks a source id of a provider. It is the one place the
// formats live: a TMDB id is a positive int32, an Open Library id a work id.
func ValidSourceID(provider, id string) error {
	field := sourceIDField(provider)
	switch provider {
	case ProviderTMDB:
		n, err := strconv.Atoi(id)
		switch {
		case err != nil || n <= 0:
			return apperr.Validation(apperr.FieldError{Field: field, Message: "must be greater than zero"})
		case n > math.MaxInt32:
			return apperr.Validation(apperr.FieldError{Field: field, Message: "is too large"})
		}
	case ProviderOpenLibrary:
		if !openLibraryWorkRe.MatchString(id) || len(id) > 64 {
			return apperr.Validation(apperr.FieldError{Field: field, Message: "must be an Open Library work id such as OL45804W"})
		}
	default:
		return fmt.Errorf("unknown lookup provider %q", provider)
	}
	return nil
}

// identity is the item metadata key that stores a provider's id and its value:
// tmdb_id is a number, openlibrary_id a string. The id is part of every
// successful match (the weekly refresh relies on it).
func identity(provider, sourceID string) (string, any) {
	if provider == ProviderTMDB {
		n, _ := strconv.Atoi(sourceID)
		return "tmdb_id", n
	}
	return "openlibrary_id", sourceID
}

// ProviderError maps a provider failure to the API errors, the single place for
// it: ErrDisabled is 503 lookup_disabled, anything else 503 upstream_unavailable.
func ProviderError(err error) error {
	if errors.Is(err, ErrDisabled) {
		return apperr.LookupDisabled()
	}
	return apperr.UpstreamUnavailable()
}
