package lookup

import (
	"context"
	"errors"
	"strconv"

	"github.com/0xfederama/collections-app-backend/internal/integrations/tmdb"
)

// tmdbProvider adapts the TMDB client (movie and TV, retention D-089, streaming
// providers by country, posters) to Provider.
type tmdbProvider struct{ client *tmdb.Client }

// NewTMDBProvider wraps a TMDB client; a nil or keyless client is simply disabled.
func NewTMDBProvider(client *tmdb.Client) Provider { return tmdbProvider{client: client} }

func (tmdbProvider) Name() string { return ProviderTMDB }

func (p tmdbProvider) Enabled() bool { return p.client.Enabled() }

func (tmdbProvider) ValidateSourceID(id string) error { return ValidSourceID(ProviderTMDB, id) }

func (p tmdbProvider) Search(ctx context.Context, typ, query, language string) ([]Suggestion, error) {
	matches, err := p.client.Search(ctx, typ, query, language)
	if err != nil {
		return nil, tmdbError(err)
	}
	out := make([]Suggestion, 0, len(matches))
	for _, m := range matches {
		out = append(out, Suggestion{
			SourceID: strconv.Itoa(m.ID), Title: m.Title, OriginalTitle: m.OriginalTitle,
			Year: m.Year, ImageURL: m.PosterURL, Overview: m.Overview,
		})
	}
	return out, nil
}

func (p tmdbProvider) Details(ctx context.Context, typ, sourceID, language, country string) (Details, error) {
	id, err := strconv.Atoi(sourceID)
	if err != nil {
		return Details{}, errors.New("invalid TMDB id")
	}
	d, err := p.client.Details(ctx, typ, id, language, country)
	if err != nil {
		return Details{}, tmdbError(err)
	}
	return Details{SourceID: strconv.Itoa(d.ID), Title: d.Title, Fields: d.Metadata, ImagePath: d.PosterPath}, nil
}

func (p tmdbProvider) DownloadImage(ctx context.Context, imagePath string) ([]byte, error) {
	b, err := p.client.DownloadPoster(ctx, imagePath)
	return b, tmdbError(err)
}

func tmdbError(err error) error {
	if errors.Is(err, tmdb.ErrDisabled) {
		return ErrDisabled
	}
	return err
}
