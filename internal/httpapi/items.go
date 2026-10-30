package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/items/core"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
)

func (s *Server) LookupItemMetadata(ctx context.Context, r oapi.LookupItemMetadataRequestObject) (oapi.LookupItemMetadataResponseObject, error) {
	if s.deps.Lookup == nil {
		return nil, apperr.LookupDisabled()
	}
	mode := lookup.ModeFill
	if r.Body.Mode != nil {
		mode = lookup.Mode(*r.Body.Mode)
	}
	// tmdb_id selects a TMDB match (movie and TV), source_id an Open Library work (book).
	// The service uses the one that fits the item's type and rejects a malformed id.
	it, err := s.deps.Lookup.Apply(ctx, r.Id, IfMatchVersion(ctx), mode, lookup.Selection{TMDBID: r.Body.TmdbId, SourceID: r.Body.SourceId})
	if err != nil {
		return nil, err
	}
	body, err := itemBody(it)
	if err != nil {
		return nil, err
	}
	return oapi.LookupItemMetadata200JSONResponse{
		Body: body, Headers: oapi.LookupItemMetadata200ResponseHeaders{ETag: itemETag(it)},
	}, nil
}

func itemETag(it core.Item) string { return etag.Format(etag.Item, it.Version) }

func priceBody(p *core.Price) nullable.Nullable[oapi.Price] {
	if p == nil {
		return nullable.NewNullNullable[oapi.Price]()
	}
	return nullable.NewNullableWithValue(oapi.Price{Min: nullableString(p.Min), Max: nullableString(p.Max), Currency: p.Currency})
}

func imageBody(img *core.Image) (nullable.Nullable[oapi.Image], error) {
	if img == nil {
		return nullable.NewNullNullable[oapi.Image](), nil
	}
	var out oapi.Image
	var err error
	if img.Source == core.ImageSourceUpload {
		err = out.FromUploadImage(oapi.UploadImage{
			Source: oapi.Upload, AttachmentId: img.AttachmentID, Url: img.URL, ThumbnailUrl: img.ThumbnailURL,
			Width: img.Width, Height: img.Height, OriginalFilename: nullableString(img.OriginalFilename),
		})
	} else {
		err = out.FromUrlImage(oapi.UrlImage{Source: oapi.Url, Url: img.URL})
	}
	if err != nil {
		return nullable.Nullable[oapi.Image]{}, fmt.Errorf("encode item image: %w", err)
	}
	return nullable.NewNullableWithValue(out), nil
}

func itemBody(it core.Item) (oapi.Item, error) {
	image, err := imageBody(it.Image)
	if err != nil {
		return oapi.Item{}, err
	}
	var metadata map[string]interface{}
	if len(it.Metadata) > 0 {
		if err := json.Unmarshal(it.Metadata, &metadata); err != nil {
			return oapi.Item{}, fmt.Errorf("decode item metadata: %w", err)
		}
	}
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	out := oapi.Item{
		Id: it.ID, ListId: it.ListID, Title: it.Title, Type: nullableString(it.Type), Metadata: metadata,
		Image: image, Price: priceBody(it.Price), Website: nullableString(it.Website),
		ContentVersion: it.ContentVersion, SortOrder: it.SortOrder, Version: it.Version,
		CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
	}
	if it.Path != nil {
		path := make([]oapi.PathSegment, 0, len(it.Path))
		for _, p := range it.Path {
			path = append(path, oapi.PathSegment{Type: oapi.PathSegmentType(p.Type), Id: p.ID, Name: p.Name})
		}
		out.Path = &path
	}
	return out, nil
}

// itemSummaryBody is the item row of search and recent items (the list route moved to Huma).
func itemSummaryBody(it core.Item) oapi.ItemSummary {
	out := oapi.ItemSummary{
		Id: it.ID, ListId: it.ListID, Title: it.Title, Type: nullableString(it.Type),
		Price: priceBody(it.Price), Website: nullableString(it.Website), ContentVersion: it.ContentVersion,
		SortOrder: it.SortOrder, Version: it.Version, CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
		Image: nullable.NewNullNullable[oapi.ImageSummary](),
	}
	// The stored metadata is validated JSON written by this service; anything unreadable degrades to {}.
	out.Metadata = map[string]any{}
	if len(it.Metadata) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(it.Metadata, &meta); err == nil && meta != nil {
			out.Metadata = meta
		}
	}
	if img := it.Image; img != nil {
		sum := oapi.ImageSummary{Url: img.URL}
		if img.Source == core.ImageSourceUpload {
			thumb := img.ThumbnailURL
			sum.ThumbnailUrl = &thumb
		}
		out.Image = nullable.NewNullableWithValue(sum)
	}
	return out
}
