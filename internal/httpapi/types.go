package httpapi

import (
	"context"

	"github.com/oapi-codegen/nullable"

	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
)

func typeBody(k definitions.Type) oapi.TypeDefinition {
	fs := make([]oapi.TypeField, len(k.Fields))
	for i, f := range k.Fields {
		fs[i] = oapi.TypeField{Key: f.Key, Label: f.Label, Type: oapi.TypeFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}
	}
	out := oapi.TypeDefinition{Key: k.Key, Name: k.Name, AllowsPrice: k.AllowsPrice, IsLocked: k.Locked, Version: k.Version, Fields: fs}
	if k.DefaultIcon == nil {
		out.DefaultIcon = nullable.NewNullNullable[string]()
	} else {
		out.DefaultIcon = nullable.NewNullableWithValue(*k.DefaultIcon)
	}
	if k.DefaultSortField == nil {
		out.DefaultSortField = nullable.NewNullNullable[string]()
	} else {
		out.DefaultSortField = nullable.NewNullableWithValue(*k.DefaultSortField)
	}
	if k.DefaultPinnedFields == nil {
		out.DefaultPinnedFields = nullable.NewNullNullable[[]string]()
	} else {
		out.DefaultPinnedFields = nullable.NewNullableWithValue(*k.DefaultPinnedFields)
	}
	return out
}
func (s *Server) ListTypes(ctx context.Context, _ oapi.ListTypesRequestObject) (oapi.ListTypesResponseObject, error) {
	ks, e := s.deps.Definitions.List(ctx)
	if e != nil {
		return nil, e
	}
	out := make(oapi.ListTypes200JSONResponse, len(ks))
	for i, k := range ks {
		out[i] = typeBody(k)
	}
	return out, nil
}
func (s *Server) CreateType(ctx context.Context, r oapi.CreateTypeRequestObject) (oapi.CreateTypeResponseObject, error) {
	var defaultIcon *string
	if value, err := r.Body.DefaultIcon.Get(); err == nil {
		defaultIcon = &value
	}
	var defaultSort *string
	if value, err := r.Body.DefaultSortField.Get(); err == nil {
		defaultSort = &value
	}
	var defaultPins *[]string
	if value, err := r.Body.DefaultPinnedFields.Get(); err == nil {
		defaultPins = &value
	}
	k, e := s.deps.Definitions.Create(ctx, definitions.CreateType{
		Name: r.Body.Name, DefaultIcon: defaultIcon, AllowsPrice: r.Body.AllowsPrice != nil && *r.Body.AllowsPrice,
		DefaultSortField: defaultSort, DefaultPinnedFields: defaultPins,
	})
	if e != nil {
		return nil, e
	}
	return oapi.CreateType201JSONResponse{Body: typeBody(k), Headers: oapi.CreateType201ResponseHeaders{ETag: etag.Format(etag.Type, k.Version), Location: "/api/v1/types/" + k.Key}}, nil
}
func (s *Server) UpdateType(ctx context.Context, r oapi.UpdateTypeRequestObject) (oapi.UpdateTypeResponseObject, error) {
	k, e := s.deps.Definitions.Update(ctx, r.Key, IfMatchVersion(ctx), definitions.UpdateType{
		Name: r.Body.Name, AllowsPrice: r.Body.AllowsPrice,
		DefaultIcon:         typeNullableString(r.Body.DefaultIcon),
		DefaultSortField:    typeNullableString(r.Body.DefaultSortField),
		DefaultPinnedFields: typeNullableStrings(r.Body.DefaultPinnedFields),
	})
	if e != nil {
		return nil, e
	}
	return oapi.UpdateType200JSONResponse{Body: typeBody(k), Headers: oapi.UpdateType200ResponseHeaders{ETag: etag.Format(etag.Type, k.Version)}}, nil
}

func typeNullableString(value nullable.Nullable[string]) definitions.NullableString {
	if !value.IsSpecified() {
		return definitions.NullableString{}
	}
	if value.IsNull() {
		return definitions.NullableString{Set: true}
	}
	v, _ := value.Get()
	return definitions.NullableString{Set: true, Value: &v}
}

func typeNullableStrings(value nullable.Nullable[[]string]) definitions.NullableStrings {
	if !value.IsSpecified() {
		return definitions.NullableStrings{}
	}
	if value.IsNull() {
		return definitions.NullableStrings{Set: true}
	}
	v, _ := value.Get()
	return definitions.NullableStrings{Set: true, Value: &v}
}
func (s *Server) DeleteType(ctx context.Context, r oapi.DeleteTypeRequestObject) (oapi.DeleteTypeResponseObject, error) {
	if e := s.deps.Definitions.Delete(ctx, r.Key, IfMatchVersion(ctx)); e != nil {
		return nil, e
	}
	return oapi.DeleteType204Response{}, nil
}
func (s *Server) CreateTypeField(ctx context.Context, r oapi.CreateTypeFieldRequestObject) (oapi.CreateTypeFieldResponseObject, error) {
	f, err := s.deps.Definitions.CreateField(ctx, r.Key, IfMatchVersion(ctx), definitions.CreateField{Label: r.Body.Label, Type: string(r.Body.Type)})
	if err != nil {
		return nil, err
	}
	return oapi.CreateTypeField201JSONResponse{Body: oapi.TypeField{Key: f.Key, Label: f.Label, Type: oapi.TypeFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}, Headers: oapi.CreateTypeField201ResponseHeaders{ETag: etag.Format(etag.Field, f.Version)}}, nil
}
func (s *Server) ReorderTypeFields(ctx context.Context, r oapi.ReorderTypeFieldsRequestObject) (oapi.ReorderTypeFieldsResponseObject, error) {
	version, err := s.deps.Definitions.ReorderFields(ctx, r.Key, IfMatchVersion(ctx), r.Body.Keys)
	if err != nil {
		return nil, err
	}
	return oapi.ReorderTypeFields204Response{Headers: oapi.ReorderTypeFields204ResponseHeaders{ETag: etag.Format(etag.Type, version)}}, nil
}
func (s *Server) UpdateTypeField(ctx context.Context, r oapi.UpdateTypeFieldRequestObject) (oapi.UpdateTypeFieldResponseObject, error) {
	f, err := s.deps.Definitions.RenameField(ctx, r.Key, r.FieldKey, IfMatchVersion(ctx), r.Body.Label)
	if err != nil {
		return nil, err
	}
	return oapi.UpdateTypeField200JSONResponse{Body: oapi.TypeField{Key: f.Key, Label: f.Label, Type: oapi.TypeFieldType(f.Type), Position: f.Position, IsLocked: f.Locked, Version: f.Version}, Headers: oapi.UpdateTypeField200ResponseHeaders{ETag: etag.Format(etag.Field, f.Version)}}, nil
}
func (s *Server) PreviewDeleteTypeField(ctx context.Context, r oapi.PreviewDeleteTypeFieldRequestObject) (oapi.PreviewDeleteTypeFieldResponseObject, error) {
	count, err := s.deps.Definitions.FieldDeletePreview(ctx, r.Key, r.FieldKey)
	if err != nil {
		return nil, err
	}
	return oapi.PreviewDeleteTypeField200JSONResponse{PopulatedItemCount: count}, nil
}
func (s *Server) DeleteTypeField(ctx context.Context, r oapi.DeleteTypeFieldRequestObject) (oapi.DeleteTypeFieldResponseObject, error) {
	count, err := s.deps.Definitions.DeleteField(ctx, r.Key, r.FieldKey, IfMatchVersion(ctx))
	if err != nil {
		return nil, err
	}
	return oapi.DeleteTypeField200JSONResponse{PopulatedItemCount: count}, nil
}
