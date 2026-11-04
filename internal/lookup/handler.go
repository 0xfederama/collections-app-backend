package lookup

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Register adds the lookup operations to the API: the movie and TV suggestions,
// the book suggestions and the item lookup. The handlers do HTTP only: parse the
// input, call the service or the provider, shape the output. Errors go back as
// they are: httpserver.Op maps them to problems. A nil svc (lookup not
// configured) answers 503 lookup_disabled; a nil or disabled provider answers
// the normal `available: false` suggestion state.
func Register(api huma.API, log *slog.Logger, svc *Service, tmdb, books Provider, settings SettingsReader) {
	h := &handler{svc: svc, tmdb: tmdb, books: books, settings: settings, log: log}
	tmdbTags := []string{"tmdb"}

	huma.Register(api, huma.Operation{
		OperationID: "listTmdbSuggestions", Method: http.MethodGet, Path: "/api/v1/tmdb/suggestions", Tags: tmdbTags,
		Summary: "Suggest movie or TV matches",
		Description: "Searches TMDB using the workspace's `metadata_language`. When the server has no TMDB key, returns " +
			"`{ \"available\": false, \"results\": [] }`; this endpoint does not fail merely because lookup is disabled. " +
			"Results retain TMDB's ranking. Titles are in Latin script: the localized title when it is Latin, else the English title, " +
			"else the Latin original; a non-Latin title is returned only when TMDB has no Latin form. " +
			"The returned poster URL is for suggestion display only and is not an item image.",
		Errors: []int{http.StatusBadRequest, http.StatusServiceUnavailable},
	}, httpserver.Op(log, h.tmdbSuggestions))
	huma.Register(api, huma.Operation{
		OperationID: "searchBookSuggestions", Method: http.MethodGet, Path: "/api/v1/books/suggestions", Tags: []string{"books"},
		Summary: "Suggest book matches",
		Description: "Searches Open Library using the workspace's `metadata_language` as a language preference. A query that is a bare ISBN " +
			"(10 or 13 digits, hyphens allowed) searches by ISBN. When book lookup is turned off on the server (`book_lookup_available` " +
			"is `false` in the settings), returns `{ \"available\": false, \"results\": [] }`; this endpoint does not fail merely because " +
			"lookup is disabled. Results retain Open Library's ranking. `source_id` is the Open Library work id used by item create and " +
			"`POST /items/{id}/lookup`. `pages` is a median over editions, and `cover_url` is for suggestion display only, not an item image.",
		Errors: []int{http.StatusBadRequest, http.StatusServiceUnavailable},
	}, httpserver.Op(log, h.bookSuggestions))
	huma.Register(api, huma.Operation{
		OperationID: "lookupItemMetadata", Method: http.MethodPost, Path: "/api/v1/items/{id}/lookup", Tags: tmdbTags,
		Summary: "Fill, replace or refresh an item's looked-up metadata",
		Description: "Requires `If-Match` (`\"i-N\"`) and is available only for movie, TV and book items (TMDB for movie and TV, Open Library for books). " +
			"`fill` preserves every non-empty user value and fills empty fields; `rematch` searches from the current title unless `tmdb_id` " +
			"(movie and TV) or `source_id` (book) selects a suggestion, then replaces the title and all fields fetched from the provider, " +
			"user edits to those fields included. `refresh` (movie and TV only, otherwise `422 validation_failed`) re-fetches the item's TMDB id, " +
			"or the `tmdb_id` given, and overwrites the title and every fetched field, user edits included, as the weekly refresh does; a field " +
			"TMDB no longer returns is removed. An item's `lookup_disabled` flag does not stop an explicit refresh. A refresh that fails changes nothing. " +
			"All modes preserve an existing item image. A poster or cover is stored through the normal " +
			"image pipeline only when the item has no image. On a book, `fill` also sets `stores` to `[preferred_store]` (the workspace default " +
			"store) when `stores` is empty; no mode ever removes or replaces the user's store choices. The default mode is `fill`. When a search " +
			"has no result, the operation returns the unchanged item and its current ETag. An explicit lookup while the integration is disabled " +
			"or the provider cannot be reached returns `503 lookup_disabled` or `503 upstream_unavailable` respectively. The server keeps the " +
			"provenance of imported fields and posters. Movie and TV items are also refreshed from TMDB once a week by the server, which " +
			"overwrites every fetched field and the title and keeps a user-supplied image; an item's `lookup_disabled` flag stops that refresh. " +
			"Open Library data is never refreshed: it is kept until the user changes it.",
		DefaultStatus: http.StatusOK,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired, http.StatusServiceUnavailable},
		Middlewares: huma.Middlewares{httpserver.IfMatchFirst(api, etag.Item)},
	}, httpserver.Op(log, h.lookupItem))
	huma.Register(api, huma.Operation{
		OperationID: "refreshListLookups", Method: http.MethodPost, Path: "/api/v1/lists/{id}/lookup-refresh", Tags: []string{"lookup"},
		Summary: "Refresh the TMDB details of a list's movie and TV items",
		Description: "Refreshes every active movie and TV item of the list that has a TMDB id and is not `lookup_disabled`, one at a time through " +
			"the server's TMDB pacing, with the weekly refresh's rule: the title and every fetched field are overwritten, user edits included, " +
			"and a user-supplied image is kept. Books, items without a TMDB id and `lookup_disabled` items are skipped. No `If-Match` is needed: " +
			"each item is versioned internally, and an item changed during the run is counted as skipped. Answers the counts. Each call refreshes at most 50 items, oldest fetch first; `remaining` counts the eligible items left for later calls, so run again until it is 0. A TMDB rate limit " +
			"or outage stops the run, and the items not yet refreshed are counted as failed; a failed item keeps its values. Unknown or trashed " +
			"lists answer 404. TMDB disabled on the server answers `503 lookup_disabled`.",
		DefaultStatus: http.StatusOK,
		Errors:        []int{http.StatusNotFound, http.StatusServiceUnavailable},
	}, httpserver.Op(log, h.refreshList))
}

type handler struct {
	svc      *Service
	tmdb     Provider
	books    Provider
	settings SettingsReader
	log      *slog.Logger
}

// TmdbSuggestionsInput is the listTmdbSuggestions query. Huma reads an empty value
// as absent, so a present but empty q is the same 400 as a missing one.
type TmdbSuggestionsInput struct {
	Type string `query:"type" enum:"movie,tv_show" required:"true" doc:"Movie or TV show."`
	Q    string `query:"q" minLength:"1" maxLength:"300" required:"true" doc:"Title to search, 1 to 300 characters."`
}

// TmdbSuggestionsOutput is the listTmdbSuggestions answer.
type TmdbSuggestionsOutput struct {
	Body TmdbSuggestionsJSON
}

func (h *handler) tmdbSuggestions(ctx context.Context, in *TmdbSuggestionsInput) (*TmdbSuggestionsOutput, error) {
	if h.tmdb == nil || !h.tmdb.Enabled() {
		return &TmdbSuggestionsOutput{Body: TmdbSuggestionsJSON{Results: []TmdbSuggestionJSON{}}}, nil
	}
	if strings.TrimSpace(in.Q) == "" {
		return nil, blankQuery()
	}
	settings, err := h.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	matches, err := h.tmdb.Search(ctx, in.Type, in.Q, settings.MetadataLanguage)
	if errors.Is(err, ErrDisabled) {
		return &TmdbSuggestionsOutput{Body: TmdbSuggestionsJSON{Results: []TmdbSuggestionJSON{}}}, nil
	}
	if err != nil {
		h.warn(ctx, "TMDB suggestions failed", err)
		return nil, apperr.UpstreamUnavailable()
	}
	out := TmdbSuggestionsJSON{Available: true, Results: make([]TmdbSuggestionJSON, 0, len(matches))}
	for _, m := range matches {
		out.Results = append(out.Results, tmdbSuggestion(m))
	}
	return &TmdbSuggestionsOutput{Body: out}, nil
}

// BookSuggestionsInput is the searchBookSuggestions query.
type BookSuggestionsInput struct {
	Q string `query:"q" minLength:"1" maxLength:"300" required:"true" doc:"Title, author or ISBN to search, 1 to 300 characters."`
}

// BookSuggestionsOutput is the searchBookSuggestions answer.
type BookSuggestionsOutput struct {
	Body BookSuggestionsJSON
}

func (h *handler) bookSuggestions(ctx context.Context, in *BookSuggestionsInput) (*BookSuggestionsOutput, error) {
	if h.books == nil || !h.books.Enabled() {
		return &BookSuggestionsOutput{Body: BookSuggestionsJSON{Results: []BookSuggestionJSON{}}}, nil
	}
	if strings.TrimSpace(in.Q) == "" {
		return nil, blankQuery()
	}
	settings, err := h.settings.Get(ctx)
	if err != nil {
		return nil, err
	}
	matches, err := h.books.Search(ctx, types.Book, in.Q, settings.MetadataLanguage)
	if errors.Is(err, ErrDisabled) {
		return &BookSuggestionsOutput{Body: BookSuggestionsJSON{Results: []BookSuggestionJSON{}}}, nil
	}
	if err != nil {
		h.warn(ctx, "book suggestions failed", err)
		return nil, apperr.UpstreamUnavailable()
	}
	out := BookSuggestionsJSON{Available: true, Results: make([]BookSuggestionJSON, 0, len(matches))}
	for _, m := range matches {
		out.Results = append(out.Results, bookSuggestion(m))
	}
	return &BookSuggestionsOutput{Body: out}, nil
}

// blankQuery answers a q that is only whitespace: the schema cannot see it.
func blankQuery() error {
	return problem.New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter",
		`"q" must contain a non-whitespace character.`)
}

// warn logs a failed suggestion search. The upstream detail stays in the log only.
func (h *handler) warn(ctx context.Context, msg string, err error) {
	if h.log != nil {
		h.log.WarnContext(ctx, msg, "err", err)
	}
}

// IDParam is the {id} path parameter of the item lookup.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

// LookupItemInput is the lookupItemMetadata path, If-Match and body.
type LookupItemInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
	Body    LookupItemRequest
}

// ListLookupRefreshOutput is the refreshListLookups answer.
type ListLookupRefreshOutput struct {
	Body ListLookupRefreshJSON
}

// LookupItemOutput is the updated item with its ETag.
type LookupItemOutput struct {
	ETag string `header:"ETag"`
	Body items.ItemJSON
}

func (h *handler) lookupItem(ctx context.Context, in *LookupItemInput) (*LookupItemOutput, error) {
	if h.svc == nil {
		return nil, apperr.LookupDisabled()
	}
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	mode := ModeFill
	if in.Body.Mode != nil {
		mode = Mode(*in.Body.Mode)
	}
	// tmdb_id selects a TMDB match (movie and TV), source_id an Open Library work (book).
	// The service uses the one that fits the item's type and rejects a malformed id.
	it, err := h.svc.Apply(ctx, in.ID, version, mode, Selection{TMDBID: in.Body.TmdbID, SourceID: in.Body.SourceID})
	if err != nil {
		return nil, err
	}
	body, err := items.ToItemJSON(it)
	if err != nil {
		return nil, err
	}
	return &LookupItemOutput{ETag: etag.Format(etag.Item, it.Version), Body: body}, nil
}

// refreshList is the refreshListLookups operation: the counts of one list refresh.
func (h *handler) refreshList(ctx context.Context, in *IDParam) (*ListLookupRefreshOutput, error) {
	if h.svc == nil {
		return nil, apperr.LookupDisabled()
	}
	result, err := h.svc.RefreshList(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &ListLookupRefreshOutput{Body: ListLookupRefreshJSON{Failed: result.Failed, Remaining: result.Remaining, Skipped: result.Skipped, Updated: result.Updated}}, nil
}
