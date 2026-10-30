package trash

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

// Register adds the trash operations to the API, and the delete operations of
// folders, lists and items (they move rows into the trash). The handlers do HTTP
// only: parse the input, call the service, shape the output. Errors go back as
// they are: httpserver.Op maps them to problems.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}

	huma.Register(api, huma.Operation{
		OperationID: "deleteFolder", Method: http.MethodDelete, Path: "/api/v1/folders/{id}", Tags: []string{"folders"},
		Summary:       "Move a folder and its subtree to the trash",
		Description:   "Requires `If-Match` (`\"f-N\"`).",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		Middlewares:   huma.Middlewares{ifMatchFirst(api, etag.Folder)},
	}, httpserver.Op(log, h.deleteFolder))
	huma.Register(api, huma.Operation{
		OperationID: "deleteList", Method: http.MethodDelete, Path: "/api/v1/lists/{id}", Tags: []string{"lists"},
		Summary:       "Move a list and its items to the trash",
		Description:   "Requires `If-Match` (`\"l-N\"`).",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		Middlewares:   huma.Middlewares{ifMatchFirst(api, etag.List)},
	}, httpserver.Op(log, h.deleteList))
	huma.Register(api, huma.Operation{
		OperationID: "deleteItem", Method: http.MethodDelete, Path: "/api/v1/items/{id}", Tags: []string{"items"},
		Summary:       "Move an item to the trash",
		Description:   "Requires `If-Match` (`\"i-N\"`).",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
		Middlewares:   huma.Middlewares{ifMatchFirst(api, etag.Item)},
	}, httpserver.Op(log, h.deleteItem))
	huma.Register(api, huma.Operation{
		OperationID: "listTrash", Method: http.MethodGet, Path: "/api/v1/trash", Tags: []string{"trash"},
		Summary:     "List trash entries",
		Description: "One entry per user-initiated delete, newest first.",
		Errors:      []int{http.StatusBadRequest},
	}, httpserver.Op(log, h.list))
	huma.Register(api, huma.Operation{
		OperationID: "restoreTrashBatch", Method: http.MethodPost, Path: "/api/v1/trash/{batch_id}/restore", Tags: []string{"trash"},
		Summary: "Restore a trash batch",
		Description: "No `If-Match`. The body is optional. Without a destination the root returns to its original parent; if that parent is " +
			"trashed or gone the call returns `409 parent_in_trash` (with `blocking_parent`). With a destination the root is re-parented and " +
			"appended to the end: `destination.folder_id` for folders and lists (`null` = root), `destination.list_id` for items.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.restore))
	huma.Register(api, huma.Operation{
		OperationID: "purgeTrashBatch", Method: http.MethodDelete, Path: "/api/v1/trash/{batch_id}", Tags: []string{"trash"},
		Summary:       "Purge a trash batch permanently",
		Description:   "Deletes the subtree and its files. No `If-Match`.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.purge))
	huma.Register(api, huma.Operation{
		// The operationId is the one of the contract (api/openapi.yaml), which calls it emptyTrash.
		OperationID: "emptyTrash", Method: http.MethodDelete, Path: "/api/v1/trash", Tags: []string{"trash"},
		Summary: "Empty the trash",
		Description: "Permanently deletes every trash batch and their files, the same as purging each batch. No `If-Match`.\n" +
			"Idempotent: an empty trash also answers 204.",
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{http.StatusInternalServerError},
	}, httpserver.Op(log, h.purgeAll))
}

type handler struct{ svc *Service }

// ifMatchFirst answers 428 for a missing or malformed If-Match before the body is
// read: a broken request must not hide the missing precondition.
func ifMatchFirst(api huma.API, prefix string) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		if _, ok := etag.Parse(ctx.Header("If-Match"), prefix); !ok {
			_ = huma.WriteErr(api, ctx, http.StatusPreconditionRequired, "", problem.PreconditionRequired())
			return
		}
		next(ctx)
	}
}

// IDParam is the {id} path parameter of the delete operations.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

// DeleteFolderInput is the deleteFolder path and If-Match.
type DeleteFolderInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Folder ETag, \"f-N\"."`
}

// DeleteListInput is the deleteList path and If-Match.
type DeleteListInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"List ETag, \"l-N\"."`
}

// DeleteItemInput is the deleteItem path and If-Match.
type DeleteItemInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
}

func (h *handler) deleteFolder(ctx context.Context, in *DeleteFolderInput) (*struct{}, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Folder)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	if err := h.svc.DeleteFolder(ctx, in.ID, version); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *handler) deleteList(ctx context.Context, in *DeleteListInput) (*struct{}, error) {
	version, ok := etag.Parse(in.IfMatch, etag.List)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	if err := h.svc.DeleteList(ctx, in.ID, version); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *handler) deleteItem(ctx context.Context, in *DeleteItemInput) (*struct{}, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	if err := h.svc.DeleteItem(ctx, in.ID, version); err != nil {
		return nil, err
	}
	return nil, nil
}

// ListTrashInput is the listTrash query. Huma reads an empty value as absent, so
// Resolve keeps the old rule that a present but empty parameter is a 400.
type ListTrashInput struct {
	Limit  int `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
	Offset int `query:"offset" default:"0" minimum:"0" doc:"Number of rows to skip."`
}

// emptyRejected are the query parameters that are rejected when present and empty.
var emptyRejected = []string{"limit", "offset"}

// Resolve answers 400 invalid_parameter for an empty value of a paging parameter.
func (in *ListTrashInput) Resolve(ctx huma.Context) []error {
	u := ctx.URL()
	query := u.Query()
	var errs []error
	for _, name := range emptyRejected {
		if vs, ok := query[name]; ok && len(vs) > 0 && vs[0] == "" {
			errs = append(errs, &huma.ErrorDetail{Location: "query." + name, Message: "must not be empty"})
		}
	}
	return errs
}

// TrashPageOutput is the listTrash answer.
type TrashPageOutput struct {
	Body TrashPageJSON
}

func (h *handler) list(ctx context.Context, in *ListTrashInput) (*TrashPageOutput, error) {
	page, err := h.svc.List(ctx, in.Limit, in.Offset)
	if err != nil {
		return nil, err
	}
	out := TrashPageJSON{Data: make([]TrashEntryJSON, 0, len(page.Entries)), Page: httpserver.Page{Limit: page.Limit, Offset: page.Offset, Total: page.Total}}
	for _, e := range page.Entries {
		out.Data = append(out.Data, entryJSON(e))
	}
	return &TrashPageOutput{Body: out}, nil
}

// RestoreTrashInput is the restoreTrashBatch path and optional body. A nil Body
// is a restore without destination.
type RestoreTrashInput struct {
	BatchID uuid.UUID `path:"batch_id" format:"uuid"`
	Body    *RestoreTrashRequest
}

// RestoreResultOutput is the restoreTrashBatch answer.
type RestoreResultOutput struct {
	Body RestoreResultJSON
}

func (h *handler) restore(ctx context.Context, in *RestoreTrashInput) (*RestoreResultOutput, error) {
	var dest *Destination
	if in.Body != nil && in.Body.Destination != nil {
		d := in.Body.Destination
		dest = &Destination{FolderSet: d.FolderID.Set, FolderID: d.FolderID.Value, ListID: d.ListID}
	}
	res, err := h.svc.Restore(ctx, in.BatchID, dest)
	if err != nil {
		return nil, err
	}
	return &RestoreResultOutput{Body: RestoreResultJSON{RootID: res.RootID, RootType: res.RootType}}, nil
}

// PurgeTrashInput is the purgeTrashBatch path.
type PurgeTrashInput struct {
	BatchID uuid.UUID `path:"batch_id" format:"uuid"`
}

func (h *handler) purge(ctx context.Context, in *PurgeTrashInput) (*struct{}, error) {
	if err := h.svc.Purge(ctx, in.BatchID); err != nil {
		return nil, err
	}
	return nil, nil
}

func (h *handler) purgeAll(ctx context.Context, _ *struct{}) (*struct{}, error) {
	if _, err := h.svc.PurgeAll(ctx); err != nil {
		return nil, err
	}
	return nil, nil
}

// entryJSON is the wire shape of one trash batch. The former path is never null.
func entryJSON(e Entry) TrashEntryJSON {
	path := make([]httpserver.PathSegment, 0, len(e.FormerPath))
	for _, seg := range e.FormerPath {
		path = append(path, httpserver.PathSegment{ID: seg.ID, Name: seg.Name, Type: seg.Type})
	}
	out := TrashEntryJSON{
		DeletedAt: e.DeletedAt, FormerPath: path, ID: e.ID, RestorableInPlace: e.RestorableInPlace,
		RootID: e.RootID, RootName: e.RootName, RootType: e.RootType,
	}
	out.Counts.Folders, out.Counts.Lists, out.Counts.Items = e.Folders, e.Lists, e.Items
	return out
}
