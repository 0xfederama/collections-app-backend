package httpapi

import (
	"context"
	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/etag"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
)

func kindBody(k definitions.Kind) oapi.KindDefinition {
	fs := make([]oapi.KindField, len(k.Fields))
	for i, f := range k.Fields {
		fs[i] = oapi.KindField{Key: f.Key, Label: f.Label, Type: oapi.KindFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}
	}
	return oapi.KindDefinition{Key: k.Key, Name: k.Name, AllowsPrice: k.AllowsPrice, IsLocked: k.Locked, Version: k.Version, Fields: fs}
}
func (s *Server) ListKinds(ctx context.Context, _ oapi.ListKindsRequestObject) (oapi.ListKindsResponseObject, error) {
	ks, e := s.deps.Definitions.List(ctx)
	if e != nil {
		return nil, e
	}
	out := make(oapi.ListKinds200JSONResponse, len(ks))
	for i, k := range ks {
		out[i] = kindBody(k)
	}
	return out, nil
}
func (s *Server) CreateKind(ctx context.Context, r oapi.CreateKindRequestObject) (oapi.CreateKindResponseObject, error) {
	k, e := s.deps.Definitions.Create(ctx, definitions.CreateKind{Name: r.Body.Name, AllowsPrice: r.Body.AllowsPrice != nil && *r.Body.AllowsPrice})
	if e != nil {
		return nil, e
	}
	return oapi.CreateKind201JSONResponse{Body: kindBody(k), Headers: oapi.CreateKind201ResponseHeaders{ETag: etag.Format(etag.Kind, k.Version), Location: "/api/v1/kinds/" + k.Key}}, nil
}
func (s *Server) UpdateKind(ctx context.Context, r oapi.UpdateKindRequestObject) (oapi.UpdateKindResponseObject, error) {
	k, e := s.deps.Definitions.Rename(ctx, r.Key, IfMatchVersion(ctx), r.Body.Name)
	if e != nil {
		return nil, e
	}
	return oapi.UpdateKind200JSONResponse{Body: kindBody(k), Headers: oapi.UpdateKind200ResponseHeaders{ETag: etag.Format(etag.Kind, k.Version)}}, nil
}
func (s *Server) DeleteKind(ctx context.Context, r oapi.DeleteKindRequestObject) (oapi.DeleteKindResponseObject, error) {
	if e := s.deps.Definitions.Delete(ctx, r.Key, IfMatchVersion(ctx)); e != nil {
		return nil, e
	}
	return oapi.DeleteKind204Response{}, nil
}
func (s *Server) CreateKindField(context.Context, oapi.CreateKindFieldRequestObject) (oapi.CreateKindFieldResponseObject, error) {
	return nil, apperr.NotFound("kind field")
}
func (s *Server) ReorderKindFields(context.Context, oapi.ReorderKindFieldsRequestObject) (oapi.ReorderKindFieldsResponseObject, error) {
	return nil, apperr.NotFound("kind field")
}
func (s *Server) UpdateKindField(context.Context, oapi.UpdateKindFieldRequestObject) (oapi.UpdateKindFieldResponseObject, error) {
	return nil, apperr.NotFound("kind field")
}
func (s *Server) DeleteKindField(context.Context, oapi.DeleteKindFieldRequestObject) (oapi.DeleteKindFieldResponseObject, error) {
	return nil, apperr.NotFound("kind field")
}
