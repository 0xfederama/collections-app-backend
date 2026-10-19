package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
)

// SearchBookSuggestions returns Open Library's ranked matches in the workspace
// metadata language (F016-B3). Like the TMDB suggestions, a disabled lookup is a
// normal `available: false` state, not an error.
func (s *Server) SearchBookSuggestions(ctx context.Context, r oapi.SearchBookSuggestionsRequestObject) (oapi.SearchBookSuggestionsResponseObject, error) {
	empty := oapi.SearchBookSuggestions200JSONResponse{Available: false, Results: []oapi.BookSuggestion{}}
	if s.deps.Books == nil || !s.deps.Books.Enabled() {
		return empty, nil
	}
	if strings.TrimSpace(r.Params.Q) == "" {
		p := invalidParameter(`"q" must contain a non-whitespace character.`)
		return oapi.SearchBookSuggestions400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: oapi.BadRequestApplicationProblemPlusJSONResponse{
				Type: p.Type, Title: p.Title, Status: p.Status, Code: oapi.ProblemCode(p.Code), Detail: &p.Detail,
			},
		}, nil
	}

	settings, err := s.deps.Settings.Get(ctx, s.deps.WorkspaceID)
	if err != nil {
		return nil, err
	}
	matches, err := s.deps.Books.Search(ctx, types.Book, r.Params.Q, settings.MetadataLanguage)
	if errors.Is(err, lookup.ErrDisabled) {
		return empty, nil
	}
	if err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.WarnContext(ctx, "book suggestions failed", "err", err)
		}
		return nil, apperr.UpstreamUnavailable()
	}

	out := oapi.SearchBookSuggestions200JSONResponse{Available: true, Results: make([]oapi.BookSuggestion, 0, len(matches))}
	for _, m := range matches {
		out.Results = append(out.Results, oapi.BookSuggestion{
			SourceId: m.SourceID, Title: m.Title, Authors: m.Authors,
			Year: nullableInt(m.Year), Pages: nullableInt(m.Pages),
			CoverUrl: nullableText(m.ImageURL), Isbn: nullableText(m.ISBN),
		})
	}
	return out, nil
}
