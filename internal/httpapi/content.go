package httpapi

import (
	"context"
	"time"

	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/content"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
)

func (s *Server) GetItemContent(ctx context.Context, req oapi.GetItemContentRequestObject) (oapi.GetItemContentResponseObject, error) {
	c, err := s.deps.Content.Get(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return oapi.GetItemContent200JSONResponse{
		Body:    contentBody(c),
		Headers: oapi.GetItemContent200ResponseHeaders{ETag: etag.Format(etag.Content, c.Version)},
	}, nil
}

func (s *Server) UpdateItemContent(ctx context.Context, req oapi.UpdateItemContentRequestObject) (oapi.UpdateItemContentResponseObject, error) {
	c, err := s.deps.Content.Save(ctx, req.Id, IfMatchVersion(ctx), req.Body.Body)
	if err != nil {
		return nil, err
	}
	return oapi.UpdateItemContent200JSONResponse{
		Body:    contentBody(c),
		Headers: oapi.UpdateItemContent200ResponseHeaders{ETag: etag.Format(etag.Content, c.Version)},
	}, nil
}

func (s *Server) RestoreContentVersion(ctx context.Context, req oapi.RestoreContentVersionRequestObject) (oapi.RestoreContentVersionResponseObject, error) {
	c, err := s.deps.Content.Restore(ctx, req.Id, IfMatchVersion(ctx), req.Body.Version)
	if err != nil {
		return nil, err
	}
	return oapi.RestoreContentVersion200JSONResponse{
		Body:    contentBody(c),
		Headers: oapi.RestoreContentVersion200ResponseHeaders{ETag: etag.Format(etag.Content, c.Version)},
	}, nil
}

func (s *Server) ListContentVersions(ctx context.Context, req oapi.ListContentVersionsRequestObject) (oapi.ListContentVersionsResponseObject, error) {
	versions, err := s.deps.Content.Versions(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	data := make([]oapi.ContentVersionInfo, 0, len(versions))
	for _, v := range versions {
		data = append(data, oapi.ContentVersionInfo{Version: v.Version, SizeBytes: v.SizeBytes, CreatedAt: v.CreatedAt})
	}
	return oapi.ListContentVersions200JSONResponse{Data: data}, nil
}

func (s *Server) GetContentVersion(ctx context.Context, req oapi.GetContentVersionRequestObject) (oapi.GetContentVersionResponseObject, error) {
	c, err := s.deps.Content.Version(ctx, req.Id, req.Version)
	if err != nil {
		return nil, err
	}
	return oapi.GetContentVersion200JSONResponse{
		Body:    contentBody(c),
		Headers: oapi.GetContentVersion200ResponseHeaders{ETag: etag.Format(etag.Content, c.Version)},
	}, nil
}

// contentBody always sets updated_at: null for version 0 (a Nullable left
// unset would marshal as the zero time).
func contentBody(c content.Content) oapi.Content {
	updated := nullable.NewNullNullable[time.Time]()
	if c.UpdatedAt != nil {
		updated = nullable.NewNullableWithValue(*c.UpdatedAt)
	}
	return oapi.Content{ItemId: c.ItemID, Version: c.Version, Body: c.Body, UpdatedAt: updated}
}
