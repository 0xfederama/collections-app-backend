package httpapi

import (
	"context"
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
func (s *Server) CreateKindField(ctx context.Context, r oapi.CreateKindFieldRequestObject) (oapi.CreateKindFieldResponseObject, error) {
	f, err := s.deps.Definitions.CreateField(ctx, r.Key, IfMatchVersion(ctx), definitions.CreateField{Label: r.Body.Label, Type: string(r.Body.Type)})
	if err != nil {
		return nil, err
	}
	return oapi.CreateKindField201JSONResponse{Body: oapi.KindField{Key: f.Key, Label: f.Label, Type: oapi.KindFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}, Headers: oapi.CreateKindField201ResponseHeaders{ETag: etag.Format(etag.Field, f.Version)}}, nil
}
func (s *Server) ReorderKindFields(ctx context.Context, r oapi.ReorderKindFieldsRequestObject) (oapi.ReorderKindFieldsResponseObject, error) {
	version, err := s.deps.Definitions.ReorderFields(ctx, r.Key, IfMatchVersion(ctx), r.Body.Keys)
	if err != nil {
		return nil, err
	}
	return oapi.ReorderKindFields204Response{Headers: oapi.ReorderKindFields204ResponseHeaders{ETag: etag.Format(etag.Kind, version)}}, nil
}
func (s *Server) UpdateKindField(ctx context.Context, r oapi.UpdateKindFieldRequestObject) (oapi.UpdateKindFieldResponseObject, error) {
	f, err := s.deps.Definitions.RenameField(ctx, r.Key, r.FieldKey, IfMatchVersion(ctx), r.Body.Label)
	if err != nil {
		return nil, err
	}
	return oapi.UpdateKindField200JSONResponse{Body: oapi.KindField{Key: f.Key, Label: f.Label, Type: oapi.KindFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}, Headers: oapi.UpdateKindField200ResponseHeaders{ETag: etag.Format(etag.Field, f.Version)}}, nil
}
func (s *Server) PreviewDeleteKindField(ctx context.Context, r oapi.PreviewDeleteKindFieldRequestObject) (oapi.PreviewDeleteKindFieldResponseObject, error) {
	count, err := s.deps.Definitions.FieldDeletePreview(ctx, r.Key, r.FieldKey)
	if err != nil {
		return nil, err
	}
	return oapi.PreviewDeleteKindField200JSONResponse{PopulatedItemCount: count}, nil
}
func (s *Server) DeleteKindField(ctx context.Context, r oapi.DeleteKindFieldRequestObject) (oapi.DeleteKindFieldResponseObject, error) {
	count, err := s.deps.Definitions.DeleteField(ctx, r.Key, r.FieldKey, IfMatchVersion(ctx))
	if err != nil {
		return nil, err
	}
	return oapi.DeleteKindField200JSONResponse{PopulatedItemCount: count}, nil
}
