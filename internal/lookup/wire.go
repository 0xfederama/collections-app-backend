package lookup

import "strconv"

// The wire types below are declared outside handler.go so that the layer rules
// keep a feature's handler identifiers to itself. Their JSON keys are in
// alphabetical order, as the generated contract wrote them.

// TmdbSuggestionsJSON is the listTmdbSuggestions answer (published as "TmdbSuggestions").
// Available is false when lookup is disabled; Results is then empty, never null.
type TmdbSuggestionsJSON struct {
	Available bool                 `json:"available"`
	Results   []TmdbSuggestionJSON `json:"results" nullable:"false"`
}

// TmdbSuggestionJSON is one ranked TMDB match (published as "TmdbSuggestion").
type TmdbSuggestionJSON struct {
	Overview      string  `json:"overview"`
	OriginalTitle string  `json:"original_title"`
	PosterURL     *string `json:"poster_url" nullable:"true" format:"uri" doc:"For suggestion display only, not an item image."`
	TmdbID        int     `json:"tmdb_id" minimum:"1"`
	Title         string  `json:"title"`
	Year          *int    `json:"year" nullable:"true"`
}

// BookSuggestionsJSON is the searchBookSuggestions answer (published as "BookSuggestions").
type BookSuggestionsJSON struct {
	Available bool                 `json:"available"`
	Results   []BookSuggestionJSON `json:"results" nullable:"false"`
}

// BookSuggestionJSON is one ranked Open Library work (published as "BookSuggestion").
type BookSuggestionJSON struct {
	Authors  string  `json:"authors" doc:"The authors joined with a comma; empty when Open Library lists none."`
	CoverURL *string `json:"cover_url" nullable:"true" format:"uri" doc:"For suggestion display only, not an item image."`
	ISBN     *string `json:"isbn" nullable:"true" doc:"The first 13-digit ISBN of the work's editions, else the first ISBN."`
	Pages    *int    `json:"pages" nullable:"true" doc:"Median number of pages over the work's editions."`
	SourceID string  `json:"source_id" pattern:"^OL[0-9]+W$" doc:"The Open Library work id."`
	Title    string  `json:"title"`
	Year     *int    `json:"year" nullable:"true" doc:"First publication year."`
}

// LookupItemRequest is the body of lookupItemMetadata. Mode nil is fill. The
// rules (mode values, id formats) are the service's, so no field is constrained here.
type LookupItemRequest struct {
	Mode     *string `json:"mode,omitempty" doc:"fill (default), rematch or refresh."`
	SourceID *string `json:"source_id,omitempty" doc:"Selects a known Open Library work (book items). Without it, fill and rematch search by title and refresh uses the stored metadata.openlibrary_id."`
	TmdbID   *int    `json:"tmdb_id,omitempty" doc:"Selects a known TMDB match (movie and TV items). Without it, fill and rematch search by title and refresh uses the stored metadata.tmdb_id."`
}

// tmdbSuggestion converts one provider match to its wire shape. A zero year or an
// empty poster is null, as the contract says.
func tmdbSuggestion(m Suggestion) TmdbSuggestionJSON {
	id, _ := strconv.Atoi(m.SourceID)
	return TmdbSuggestionJSON{
		Overview: m.Overview, OriginalTitle: m.OriginalTitle, PosterURL: optionalText(m.ImageURL),
		TmdbID: id, Title: m.Title, Year: optionalInt(m.Year),
	}
}

// bookSuggestion converts one Open Library match to its wire shape.
func bookSuggestion(m Suggestion) BookSuggestionJSON {
	return BookSuggestionJSON{
		Authors: m.Authors, CoverURL: optionalText(m.ImageURL), ISBN: optionalText(m.ISBN), Pages: optionalInt(m.Pages),
		SourceID: m.SourceID, Title: m.Title, Year: optionalInt(m.Year),
	}
}

// optionalInt is null for zero.
func optionalInt(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

// optionalText is null for the empty string.
func optionalText(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
