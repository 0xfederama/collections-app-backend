package folders

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Register adds the folder operations to the API. The handlers do HTTP only:
// parse the input, call the service, shape the output. Errors go back as they
// are: httpserver.Op maps them to problems.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"folders"}

	huma.Register(api, huma.Operation{
		OperationID: "createFolder", Method: http.MethodPost, Path: "/api/v1/folders", Tags: tags,
		Summary: "Create a folder", DefaultStatus: http.StatusCreated, MaxBodyBytes: 64 << 10,
		Description: "Appended to the end of its siblings. `parent_folder_id` absent or `null` creates it at the root.",
		Errors:      []int{http.StatusBadRequest, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.create))
	huma.Register(api, huma.Operation{
		OperationID: "getFolder", Method: http.MethodGet, Path: "/api/v1/folders/{id}", Tags: tags,
		Summary: "Get a folder", Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.get))
	huma.Register(api, huma.Operation{
		OperationID: "updateFolder", Method: http.MethodPatch, Path: "/api/v1/folders/{id}", Tags: tags,
		Summary: "Rename a folder", Description: "Requires `If-Match` (`\"f-N\"`).",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.update))
	huma.Register(api, huma.Operation{
		OperationID: "moveFolder", Method: http.MethodPost, Path: "/api/v1/folders/{id}/move", Tags: tags,
		Summary: "Move a folder under another parent", Description: "Requires `If-Match`. `parent_folder_id: null` moves it to the root.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.move))
	huma.Register(api, huma.Operation{
		OperationID: "reorderFolders", Method: http.MethodPut, Path: "/api/v1/folders/order", Tags: tags,
		Summary: "Reorder the child folders of a parent", DefaultStatus: http.StatusNoContent,
		Description: "`ids` must be all active child folders of `parent_folder_id` (`null` = root), in the new order.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.reorder))
}

type handler struct{ svc *Service }

// FolderOutput is a folder with its ETag.
type FolderOutput struct {
	ETag string `header:"ETag"`
	Body FolderJSON
}

// CreatedFolderOutput adds the Location of the new folder.
type CreatedFolderOutput struct {
	ETag     string `header:"ETag"`
	Location string `header:"Location"`
	Body     FolderJSON
}

func output(f Folder) *FolderOutput {
	return &FolderOutput{ETag: etag.Format(etag.Folder, f.Version), Body: ToJSON(f)}
}

// CreateFolderRequest is the body of createFolder.
type CreateFolderRequest struct {
	Name           string     `json:"name" minLength:"1" maxLength:"200"`
	ParentFolderID *uuid.UUID `json:"parent_folder_id,omitempty" format:"uuid" nullable:"true"`
}

type createInput struct{ Body CreateFolderRequest }

func (h *handler) create(ctx context.Context, in *createInput) (*CreatedFolderOutput, error) {
	f, err := h.svc.Create(ctx, in.Body.Name, in.Body.ParentFolderID)
	if err != nil {
		return nil, err
	}
	return &CreatedFolderOutput{
		ETag: etag.Format(etag.Folder, f.Version), Location: "/api/v1/folders/" + f.ID.String(), Body: ToJSON(f),
	}, nil
}

// IDParam is the {id} path parameter.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

func (h *handler) get(ctx context.Context, in *IDParam) (*FolderOutput, error) {
	f, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return output(f), nil
}

type updateInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Folder ETag, \"f-N\"."`
	Body    UpdateFolderRequest
}

// UpdateFolderRequest is the body of updateFolder.
type UpdateFolderRequest struct {
	Name *string `json:"name,omitempty" minLength:"1" maxLength:"200"`
}

func (h *handler) update(ctx context.Context, in *updateInput) (*FolderOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Folder)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	f, err := h.svc.Rename(ctx, in.ID, version, in.Body.Name)
	if err != nil {
		return nil, err
	}
	return output(f), nil
}

type moveInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Folder ETag, \"f-N\"."`
	Body    MoveFolderRequest
}

// MoveFolderRequest is the body of moveFolder.
type MoveFolderRequest struct {
	ParentFolderID *uuid.UUID `json:"parent_folder_id" format:"uuid" nullable:"true" required:"true"`
}

func (h *handler) move(ctx context.Context, in *moveInput) (*FolderOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Folder)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	f, err := h.svc.Move(ctx, in.ID, version, in.Body.ParentFolderID)
	if err != nil {
		return nil, err
	}
	return output(f), nil
}

// ReorderFoldersRequest is the body of reorderFolders.
type ReorderFoldersRequest struct {
	ParentFolderID *uuid.UUID  `json:"parent_folder_id" format:"uuid" nullable:"true" required:"true"`
	IDs            []uuid.UUID `json:"ids" format:"uuid" nullable:"false"`
}

type reorderInput struct{ Body ReorderFoldersRequest }

func (h *handler) reorder(ctx context.Context, in *reorderInput) (*struct{}, error) {
	if err := h.svc.Reorder(ctx, in.Body.ParentFolderID, in.Body.IDs); err != nil {
		return nil, err
	}
	return nil, nil
}
