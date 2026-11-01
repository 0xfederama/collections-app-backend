package definitions

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Register adds the type and field operations to the API. The handlers do HTTP
// only: parse the input, call the manager, shape the output. Errors go back as
// they are: httpserver.Op maps them to problems. The value rules (name length,
// field types, defaults) are the manager's, so the request bodies carry no
// schema constraints of their own.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"types"}

	huma.Register(api, huma.Operation{
		OperationID: "listTypes", Method: http.MethodGet, Path: "/api/v1/types", Tags: tags,
		Summary: "List type definitions and fields",
		Errors:  []int{http.StatusInternalServerError},
	}, httpserver.Op(log, h.list))
	huma.Register(api, huma.Operation{
		OperationID: "createTypeField", Method: http.MethodPost, Path: "/api/v1/types/{key}/fields", Tags: tags,
		Summary: "Add a custom field", Description: "Requires `If-Match` on the type (`\"k-N\"`).", DefaultStatus: http.StatusCreated,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.createField))
	huma.Register(api, huma.Operation{
		OperationID: "reorderTypeFields", Method: http.MethodPut, Path: "/api/v1/types/{key}/fields/order", Tags: tags,
		Summary: "Reorder custom fields", Description: "Requires `If-Match` on the type (`\"k-N\"`). `keys` must be the custom fields, in the new order.",
		DefaultStatus: http.StatusNoContent,
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge,
			http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.reorderFields))
	huma.Register(api, huma.Operation{
		OperationID: "updateTypeField", Method: http.MethodPatch, Path: "/api/v1/types/{key}/fields/{fieldKey}", Tags: tags,
		Summary: "Rename a custom field", Description: "Requires `If-Match` on the field (`\"kf-N\"`).",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.updateField))
	huma.Register(api, huma.Operation{
		OperationID: "previewDeleteTypeField", Method: http.MethodGet, Path: "/api/v1/types/{key}/fields/{fieldKey}", Tags: tags,
		Summary: "Count item values that deleting a custom field would remove", Errors: []int{http.StatusNotFound, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.previewField))
	huma.Register(api, huma.Operation{
		OperationID: "deleteTypeField", Method: http.MethodDelete, Path: "/api/v1/types/{key}/fields/{fieldKey}", Tags: tags,
		Summary: "Delete a custom field and strip populated values", Description: "Requires `If-Match` on the field (`\"kf-N\"`).",
		Errors: []int{http.StatusNotFound, http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.removeField))
}

type handler struct{ svc *Service }

// ListTypesOutput is the listTypes answer.
type ListTypesOutput struct {
	Body []TypeDefinitionJSON
}

func (h *handler) list(ctx context.Context, _ *struct{}) (*ListTypesOutput, error) {
	types, err := h.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TypeDefinitionJSON, 0, len(types))
	for _, t := range types {
		out = append(out, typeJSON(t))
	}
	return &ListTypesOutput{Body: out}, nil
}

// KeyParam is the {key} path parameter of a type.
type KeyParam struct {
	Key string `path:"key"`
}

// FieldParam is the {key} and {fieldKey} path parameters of a field.
type FieldParam struct {
	Key      string `path:"key"`
	FieldKey string `path:"fieldKey"`
}

// FieldOutput is a field with its ETag.
type FieldOutput struct {
	ETag string `header:"ETag"`
	Body TypeFieldJSON
}

// CreatedFieldOutput is a new field with its ETag (no Location, as before the migration).
type CreatedFieldOutput struct {
	ETag string `header:"ETag"`
	Body TypeFieldJSON
}

// CreateTypeFieldRequest is the body of createTypeField. The field type is
// checked by the manager (only the custom types are accepted).
type CreateTypeFieldRequest struct {
	Label string `json:"label"`
	Type  string `json:"type"`
}

type createFieldInput struct {
	KeyParam
	IfMatch string `header:"If-Match" doc:"Type ETag, \"k-N\"."`
	Body    CreateTypeFieldRequest
}

func (h *handler) createField(ctx context.Context, in *createFieldInput) (*CreatedFieldOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Type)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	f, err := h.svc.CreateField(ctx, in.Key, version, CreateField{Label: in.Body.Label, Type: in.Body.Type})
	if err != nil {
		return nil, err
	}
	return &CreatedFieldOutput{ETag: etag.Format(etag.Field, f.Version), Body: fieldJSON(f)}, nil
}

// ReorderTypeFieldsRequest is the body of reorderTypeFields.
type ReorderTypeFieldsRequest struct {
	Keys []string `json:"keys"`
}

type reorderInput struct {
	KeyParam
	IfMatch string `header:"If-Match" doc:"Type ETag, \"k-N\"."`
	Body    ReorderTypeFieldsRequest
}

// ETagOutput is an answer with only the ETag of the type (204).
type ETagOutput struct {
	ETag string `header:"ETag"`
}

func (h *handler) reorderFields(ctx context.Context, in *reorderInput) (*ETagOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Type)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	next, err := h.svc.ReorderFields(ctx, in.Key, version, in.Body.Keys)
	if err != nil {
		return nil, err
	}
	return &ETagOutput{ETag: etag.Format(etag.Type, next)}, nil
}

// UpdateTypeFieldRequest is the body of updateTypeField.
type UpdateTypeFieldRequest struct {
	Label string `json:"label"`
}

type updateFieldInput struct {
	FieldParam
	IfMatch string `header:"If-Match" doc:"Field ETag, \"kf-N\"."`
	Body    UpdateTypeFieldRequest
}

func (h *handler) updateField(ctx context.Context, in *updateFieldInput) (*FieldOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Field)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	f, err := h.svc.RenameField(ctx, in.Key, in.FieldKey, version, in.Body.Label)
	if err != nil {
		return nil, err
	}
	return &FieldOutput{ETag: etag.Format(etag.Field, f.Version), Body: fieldJSON(f)}, nil
}

// TypeFieldDeletePreviewOutput is the count of item values a field delete removes.
type TypeFieldDeletePreviewOutput struct {
	Body TypeFieldDeletePreviewJSON
}

func (h *handler) previewField(ctx context.Context, in *FieldParam) (*TypeFieldDeletePreviewOutput, error) {
	count, err := h.svc.FieldDeletePreview(ctx, in.Key, in.FieldKey)
	if err != nil {
		return nil, err
	}
	return &TypeFieldDeletePreviewOutput{Body: TypeFieldDeletePreviewJSON{PopulatedItemCount: count}}, nil
}

type removeFieldInput struct {
	FieldParam
	IfMatch string `header:"If-Match" doc:"Field ETag, \"kf-N\"."`
}

func (h *handler) removeField(ctx context.Context, in *removeFieldInput) (*TypeFieldDeletePreviewOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Field)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	count, err := h.svc.DeleteField(ctx, in.Key, in.FieldKey, version)
	if err != nil {
		return nil, err
	}
	return &TypeFieldDeletePreviewOutput{Body: TypeFieldDeletePreviewJSON{PopulatedItemCount: count}}, nil
}
