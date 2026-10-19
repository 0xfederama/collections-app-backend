package httpapi

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
)

// ListTmdbSuggestions returns TMDB's ranked movie or TV matches in the
// workspace metadata language. A missing server-side key is a normal disabled
// state so clients can omit lookup UI without turning it into an error.
func (s *Server) ListTmdbSuggestions(ctx context.Context, r oapi.ListTmdbSuggestionsRequestObject) (oapi.ListTmdbSuggestionsResponseObject, error) {
	empty := oapi.ListTmdbSuggestions200JSONResponse{Available: false, Results: []oapi.TmdbSuggestion{}}
	if s.deps.TMDB == nil || !s.deps.TMDB.Enabled() {
		return empty, nil
	}
	if strings.TrimSpace(r.Params.Q) == "" {
		p := invalidParameter(`"q" must contain a non-whitespace character.`)
		return oapi.ListTmdbSuggestions400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: oapi.BadRequestApplicationProblemPlusJSONResponse{
				Type: p.Type, Title: p.Title, Status: p.Status, Code: oapi.ProblemCode(p.Code), Detail: &p.Detail,
			},
		}, nil
	}

	settings, err := s.deps.Settings.Get(ctx, s.deps.WorkspaceID)
	if err != nil {
		return nil, err
	}
	matches, err := s.deps.TMDB.Search(ctx, string(r.Params.Type), r.Params.Q, settings.MetadataLanguage)
	if errors.Is(err, lookup.ErrDisabled) {
		return empty, nil
	}
	if err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.WarnContext(ctx, "TMDB suggestions failed", "err", err)
		}
		return nil, apperr.UpstreamUnavailable()
	}

	out := oapi.ListTmdbSuggestions200JSONResponse{Available: true, Results: make([]oapi.TmdbSuggestion, 0, len(matches))}
	for _, match := range matches {
		id, _ := strconv.Atoi(match.SourceID)
		out.Results = append(out.Results, oapi.TmdbSuggestion{
			TmdbId:        id,
			Title:         match.Title,
			OriginalTitle: match.OriginalTitle,
			Year:          nullableInt(match.Year),
			PosterUrl:     nullableText(match.ImageURL),
			Overview:      match.Overview,
		})
	}
	return out, nil
}

func nullableInt(v int) nullable.Nullable[int] {
	if v == 0 {
		return nullable.NewNullNullable[int]()
	}
	return nullable.NewNullableWithValue(v)
}

func nullableText(v string) nullable.Nullable[string] {
	if v == "" {
		return nullable.NewNullNullable[string]()
	}
	return nullable.NewNullableWithValue(v)
}
