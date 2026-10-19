package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/etag"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/lookup"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

func (s *Server) ListItems(ctx context.Context, r oapi.ListItemsRequestObject) (oapi.ListItemsResponseObject, error) {
	p := items.ListParams{
		Sort: derefOr(r.Params.Sort), Q: derefOr(r.Params.Q),
		FilterField: derefOr(r.Params.FilterField), FilterValue: derefOr(r.Params.FilterValue),
		FilterMin: derefOr(r.Params.FilterMin), FilterMax: derefOr(r.Params.FilterMax),
	}
	if r.Params.Limit != nil {
		p.Limit = *r.Params.Limit
	}
	if r.Params.Offset != nil {
		p.Offset = *r.Params.Offset
	}
	page, err := s.deps.Items.List(ctx, r.Id, p)
	if errors.Is(err, items.ErrInvalidSort) {
		pr := invalidParameter("sort must be manual, title, updated, price (when allowed), or a field key of this list's type.")
		return oapi.ListItems400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: oapi.BadRequestApplicationProblemPlusJSONResponse{
				Type: pr.Type, Title: pr.Title, Status: pr.Status, Code: oapi.ProblemCode(pr.Code), Detail: &pr.Detail,
			},
		}, nil
	}
	if errors.Is(err, items.ErrInvalidFilter) {
		pr := invalidParameter("field filter parameters must match a sortable field's type.")
		return oapi.ListItems400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: oapi.BadRequestApplicationProblemPlusJSONResponse{
				Type: pr.Type, Title: pr.Title, Status: pr.Status, Code: oapi.ProblemCode(pr.Code), Detail: &pr.Detail,
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	out := oapi.ListItems200JSONResponse{
		Data: make([]oapi.ItemSummary, 0, len(page.Items)),
		Page: oapi.Page{Limit: page.Limit, Offset: page.Offset, Total: page.Total},
	}
	for _, it := range page.Items {
		out.Data = append(out.Data, itemSummaryBody(it))
	}
	return out, nil
}

func (s *Server) CreateItem(ctx context.Context, r oapi.CreateItemRequestObject) (oapi.CreateItemResponseObject, error) {
	b := r.Body
	in := items.CreateInput{
		Title:   b.Title,
		Price:   optionalPrice(b.Price).Value,
		Website: optionalString(b.Website).Value,
	}
	if b.Metadata != nil {
		in.Metadata = *b.Metadata
	}
	var it items.Item
	var err error
	if s.deps.Lookup != nil {
		it, err = s.deps.Lookup.Create(ctx, r.Id, in, lookup.CreateOptions{Lookup: b.Lookup, TMDBID: b.TmdbId})
	} else {
		it, err = s.deps.Items.Create(ctx, r.Id, in)
	}
	if err != nil {
		return nil, err
	}
	body, err := itemBody(it)
	if err != nil {
		return nil, err
	}
	return oapi.CreateItem201JSONResponse{
		Body:    body,
		Headers: oapi.CreateItem201ResponseHeaders{ETag: itemETag(it), Location: "/api/v1/items/" + it.ID.String()},
	}, nil
}

func (s *Server) LookupItemMetadata(ctx context.Context, r oapi.LookupItemMetadataRequestObject) (oapi.LookupItemMetadataResponseObject, error) {
	if s.deps.Lookup == nil {
		return nil, apperr.LookupDisabled()
	}
	mode := lookup.ModeFill
	if r.Body.Mode != nil {
		mode = lookup.Mode(*r.Body.Mode)
	}
	var selected *string
	if r.Body.TmdbId != nil {
		id := strconv.Itoa(*r.Body.TmdbId)
		selected = &id
	}
	it, err := s.deps.Lookup.Apply(ctx, r.Id, IfMatchVersion(ctx), mode, selected)
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

func (s *Server) GetItem(ctx context.Context, r oapi.GetItemRequestObject) (oapi.GetItemResponseObject, error) {
	it, err := s.deps.Items.Get(ctx, r.Id)
	if err != nil {
		return nil, err
	}
	body, err := itemBody(it)
	if err != nil {
		return nil, err
	}
	return oapi.GetItem200JSONResponse{Body: body, Headers: oapi.GetItem200ResponseHeaders{ETag: itemETag(it)}}, nil
}

func (s *Server) UpdateItem(ctx context.Context, r oapi.UpdateItemRequestObject) (oapi.UpdateItemResponseObject, error) {
	b := r.Body
	patch := items.Patch{
		Title:   b.Title,
		Price:   optionalPrice(b.Price),
		Website: optionalString(b.Website),
	}
	if b.Metadata != nil {
		patch.Metadata = *b.Metadata
	}
	it, err := s.deps.Items.Update(ctx, r.Id, IfMatchVersion(ctx), patch)
	if err != nil {
		return nil, err
	}
	body, err := itemBody(it)
	if err != nil {
		return nil, err
	}
	return oapi.UpdateItem200JSONResponse{Body: body, Headers: oapi.UpdateItem200ResponseHeaders{ETag: itemETag(it)}}, nil
}

func (s *Server) MoveItem(ctx context.Context, r oapi.MoveItemRequestObject) (oapi.MoveItemResponseObject, error) {
	if r.Body.ListId == uuid.Nil {
		return nil, apperr.Validation(apperr.FieldError{Field: "list_id", Message: "is required"})
	}
	it, err := s.deps.Items.Move(ctx, r.Id, IfMatchVersion(ctx), r.Body.ListId)
	if err != nil {
		return nil, err
	}
	body, err := itemBody(it)
	if err != nil {
		return nil, err
	}
	return oapi.MoveItem200JSONResponse{Body: body, Headers: oapi.MoveItem200ResponseHeaders{ETag: itemETag(it)}}, nil
}

func (s *Server) ReorderListItems(ctx context.Context, r oapi.ReorderListItemsRequestObject) (oapi.ReorderListItemsResponseObject, error) {
	if err := s.deps.Items.Reorder(ctx, r.Id, r.Body.Ids); err != nil {
		return nil, err
	}
	return oapi.ReorderListItems204Response{}, nil
}

func itemETag(it items.Item) string { return etag.Format(etag.Item, it.Version) }

// optionalPrice turns the nullable price of a request into an Optional: absent
// is not Set, `null` is Set with a nil Value (clear the price).
func optionalPrice(n nullable.Nullable[oapi.PriceInput]) tree.Optional[items.Price] {
	if !n.IsSpecified() {
		return tree.Optional[items.Price]{}
	}
	p, err := n.Get()
	if err != nil {
		return tree.Optional[items.Price]{Set: true}
	}
	out := items.Price{Currency: p.Currency, Amount: p.Amount}
	if v, err := p.Min.Get(); err == nil {
		out.Min = &v
	}
	if v, err := p.Max.Get(); err == nil {
		out.Max = &v
	}
	return tree.Optional[items.Price]{Set: true, Value: &out}
}

func priceBody(p *items.Price) nullable.Nullable[oapi.Price] {
	if p == nil {
		return nullable.NewNullNullable[oapi.Price]()
	}
	return nullable.NewNullableWithValue(oapi.Price{Min: nullableString(p.Min), Max: nullableString(p.Max), Currency: p.Currency})
}

func imageBody(img *items.Image) (nullable.Nullable[oapi.Image], error) {
	if img == nil {
		return nullable.NewNullNullable[oapi.Image](), nil
	}
	var out oapi.Image
	var err error
	if img.Source == items.ImageSourceUpload {
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

func itemBody(it items.Item) (oapi.Item, error) {
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
	// store_links is computed at read and never stored: it is not a metadata key.
	if it.StoreLinks != nil {
		metadata["store_links"] = it.StoreLinks
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

func itemSummaryBody(it items.Item) oapi.ItemSummary {
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
		if img.Source == items.ImageSourceUpload {
			thumb := img.ThumbnailURL
			sum.ThumbnailUrl = &thumb
		}
		out.Image = nullable.NewNullableWithValue(sum)
	}
	return out
}
