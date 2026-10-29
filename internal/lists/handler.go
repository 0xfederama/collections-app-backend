package lists

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// contentBodyLimit is the body limit of the operations that carry a Markdown
// description (64 KiB of text plus the JSON around it).
const contentBodyLimit = 2 << 20

// Register adds the list operations to the API. The handlers do HTTP only:
// parse the input, call the service, shape the output. Errors go back as they
// are: httpserver.Op maps them to problems. deleteList stays in the trash code.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"lists"}

	huma.Register(api, huma.Operation{
		OperationID: "createList", Method: http.MethodPost, Path: "/api/v1/lists", Tags: tags,
		Summary: "Create a list", DefaultStatus: http.StatusCreated, MaxBodyBytes: contentBodyLimit,
		Description: "Appended to the end of its siblings. `folder_id` absent or `null` creates it at the root. A missing or trashed folder " +
			"returns `409 destination_unavailable`. `type` absent or `null` is a list with no type.",
		Errors: []int{http.StatusConflict, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.create))
	huma.Register(api, huma.Operation{
		OperationID: "getList", Method: http.MethodGet, Path: "/api/v1/lists/{id}", Tags: tags,
		Summary: "Get a list", Description: "Includes `path` (the ancestor folders).", Errors: []int{http.StatusNotFound},
	}, httpserver.Op(log, h.get))
	huma.Register(api, huma.Operation{
		OperationID: "updateList", Method: http.MethodPatch, Path: "/api/v1/lists/{id}", Tags: tags,
		Summary: "Update a list", Description: "Requires `If-Match` (`\"l-N\"`). Send `description: null` to clear it. Changing `type` clears the " +
			"`metadata` of all the list's items (and their `price` when the new type has none).",
		MaxBodyBytes: contentBodyLimit,
		Errors:       []int{http.StatusNotFound, http.StatusPreconditionFailed, http.StatusPreconditionRequired, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.update))
	huma.Register(api, huma.Operation{
		OperationID: "moveList", Method: http.MethodPost, Path: "/api/v1/lists/{id}/move", Tags: tags,
		Summary: "Move a list to another folder", Description: "Requires `If-Match` (`\"l-N\"`). `folder_id: null` moves it to the root. " +
			"Appended to the end of the new siblings.",
		Errors: []int{http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed, http.StatusPreconditionRequired},
	}, httpserver.Op(log, h.move))
	huma.Register(api, huma.Operation{
		OperationID: "reorderLists", Method: http.MethodPut, Path: "/api/v1/lists/order", Tags: tags,
		Summary: "Reorder the child lists of a folder", DefaultStatus: http.StatusNoContent,
		Description: "`ids` must be all active child lists of `folder_id` (`null` = root), in the new order. No `If-Match`.",
		Errors:      []int{http.StatusNotFound, http.StatusConflict},
	}, httpserver.Op(log, h.reorder))
}

type handler struct{ svc *Service }

// ListJSON is the wire shape of a list (published as the schema "List").
type ListJSON struct {
	ID           uuid.UUID  `json:"id" format:"uuid"`
	FolderID     *uuid.UUID `json:"folder_id" format:"uuid" nullable:"true"`
	Name         string     `json:"name"`
	Type         *string    `json:"type" nullable:"true"`
	Description  *string    `json:"description" nullable:"true"`
	DisplayMode  string     `json:"display_mode" enum:"compact,thumbnail,grid"`
	SortMode     string     `json:"sort_mode"`
	PinnedFields *[]string  `json:"pinned_fields" nullable:"true"`
	Icon         *string    `json:"icon" nullable:"true"`
	IconColor    *string    `json:"icon_color" nullable:"true" enum:"red,orange,amber,green,teal,blue,indigo,purple,pink,gray"`
	SortOrder    int        `json:"sort_order"`
	ItemCount    int        `json:"item_count"`
	Version      int        `json:"version"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// PathSegmentJSON is one ancestor folder of a list (published as "PathSegment").
type PathSegmentJSON struct {
	Type string    `json:"type" enum:"folder,list"`
	ID   uuid.UUID `json:"id" format:"uuid"`
	Name string    `json:"name"`
}

// ListWithPathJSON is the getList answer: the list and its ancestor folders (published as "ListWithPath").
type ListWithPathJSON struct {
	ListJSON
	Path []PathSegmentJSON `json:"path"`
}

func toJSON(l List) ListJSON {
	out := ListJSON{
		ID: l.ID, FolderID: l.FolderID, Name: l.Name, Type: l.Type, Description: l.Description,
		DisplayMode: l.DisplayMode, SortMode: l.SortMode, Icon: l.Icon, IconColor: l.IconColor,
		SortOrder: l.SortOrder, ItemCount: l.ItemCount, Version: l.Version, CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt,
	}
	if l.PinnedFields != nil {
		keys := []string(*l.PinnedFields)
		if keys == nil {
			keys = []string{}
		}
		out.PinnedFields = &keys
	}
	return out
}

func toPathJSON(path []PathFolder) []PathSegmentJSON {
	out := make([]PathSegmentJSON, 0, len(path))
	for _, p := range path {
		out = append(out, PathSegmentJSON{Type: "folder", ID: p.ID, Name: p.Name})
	}
	return out
}

// ListOutput is a list with its ETag.
type ListOutput struct {
	ETag string `header:"ETag"`
	Body ListJSON
}

// CreatedListOutput adds the Location of the new list.
type CreatedListOutput struct {
	ETag     string `header:"ETag"`
	Location string `header:"Location"`
	Body     ListJSON
}

// ListWithPathOutput is the getList answer with its ETag.
type ListWithPathOutput struct {
	ETag string `header:"ETag"`
	Body ListWithPathJSON
}

func output(l List) *ListOutput {
	return &ListOutput{ETag: etag.Format(etag.List, l.Version), Body: toJSON(l)}
}

// CreateListRequest is the body of createList. The service checks the values
// (name length, modes, icon, description size), so the request has no schema
// constraints of its own: each rule answers with its own message.
type CreateListRequest struct {
	Name         string     `json:"name"`
	Type         *string    `json:"type,omitempty" nullable:"true"`
	FolderID     *uuid.UUID `json:"folder_id,omitempty" format:"uuid" nullable:"true"`
	Description  *string    `json:"description,omitempty" nullable:"true"`
	DisplayMode  *string    `json:"display_mode,omitempty" nullable:"true"`
	SortMode     *string    `json:"sort_mode,omitempty" nullable:"true"`
	PinnedFields []string   `json:"pinned_fields,omitempty" nullable:"true"`
	Icon         *string    `json:"icon,omitempty" nullable:"true"`
	IconColor    *string    `json:"icon_color,omitempty" nullable:"true"`
}

type createInput struct{ Body CreateListRequest }

func (h *handler) create(ctx context.Context, in *createInput) (*CreatedListOutput, error) {
	b := in.Body
	l, err := h.svc.Create(ctx, CreateInput{
		Name: b.Name, Type: b.Type, FolderID: b.FolderID, Description: b.Description,
		DisplayMode: deref(b.DisplayMode), SortMode: deref(b.SortMode),
		PinnedFields: pinnedFromWire(b.PinnedFields), Icon: b.Icon, IconColor: b.IconColor,
	})
	if err != nil {
		return nil, err
	}
	return &CreatedListOutput{
		ETag: etag.Format(etag.List, l.Version), Location: "/api/v1/lists/" + l.ID.String(), Body: toJSON(l),
	}, nil
}

// IDParam is the {id} path parameter.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

func (h *handler) get(ctx context.Context, in *IDParam) (*ListWithPathOutput, error) {
	l, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &ListWithPathOutput{
		ETag: etag.Format(etag.List, l.Version),
		Body: ListWithPathJSON{ListJSON: toJSON(l), Path: toPathJSON(l.Path)},
	}, nil
}

type updateInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"List ETag, \"l-N\"."`
	Body    UpdateListRequest
}

// UpdateListRequest is the body of updateList. Absent fields stay as they are;
// the nullable ones tell null (clear) from absent with httpserver.Optional.
type UpdateListRequest struct {
	Name         *string                       `json:"name,omitempty"`
	Type         httpserver.Optional[string]   `json:"type,omitempty"`
	Description  httpserver.Optional[string]   `json:"description,omitempty"`
	DisplayMode  *string                       `json:"display_mode,omitempty"`
	SortMode     *string                       `json:"sort_mode,omitempty"`
	PinnedFields httpserver.Optional[[]string] `json:"pinned_fields,omitempty"`
	Icon         httpserver.Optional[string]   `json:"icon,omitempty"`
	IconColor    httpserver.Optional[string]   `json:"icon_color,omitempty"`
}

func (h *handler) update(ctx context.Context, in *updateInput) (*ListOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.List)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	b := in.Body
	l, err := h.svc.Update(ctx, in.ID, version, Patch{
		Name:         b.Name,
		Type:         optional(b.Type),
		Description:  optional(b.Description),
		DisplayMode:  b.DisplayMode,
		SortMode:     b.SortMode,
		PinnedFields: pinnedOptional(b.PinnedFields),
		Icon:         optional(b.Icon),
		IconColor:    optional(b.IconColor),
	})
	if err != nil {
		return nil, err
	}
	return output(l), nil
}

type moveInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"List ETag, \"l-N\"."`
	Body    MoveListRequest
}

// MoveListRequest is the body of moveList.
type MoveListRequest struct {
	FolderID *uuid.UUID `json:"folder_id" format:"uuid" nullable:"true" required:"true"`
}

func (h *handler) move(ctx context.Context, in *moveInput) (*ListOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.List)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	l, err := h.svc.Move(ctx, in.ID, version, in.Body.FolderID)
	if err != nil {
		return nil, err
	}
	return output(l), nil
}

// ReorderListsRequest is the body of reorderLists.
type ReorderListsRequest struct {
	FolderID *uuid.UUID  `json:"folder_id" format:"uuid" nullable:"true" required:"true"`
	IDs      []uuid.UUID `json:"ids" format:"uuid" nullable:"false"`
}

type reorderInput struct{ Body ReorderListsRequest }

func (h *handler) reorder(ctx context.Context, in *reorderInput) (*struct{}, error) {
	if err := h.svc.Reorder(ctx, in.Body.FolderID, in.Body.IDs); err != nil {
		return nil, err
	}
	return nil, nil
}

// optional converts a wire PATCH field to the service's Optional.
func optional[T any](o httpserver.Optional[T]) Optional[T] {
	return Optional[T]{Set: o.Set, Value: o.Value}
}

// pinnedOptional converts the wire pinned_fields PATCH field: null is a reset to the type default.
func pinnedOptional(o httpserver.Optional[[]string]) Optional[PinnedFields] {
	out := Optional[PinnedFields]{Set: o.Set}
	if o.Value != nil {
		p := PinnedFields(*o.Value)
		out.Value = &p
	}
	return out
}

// pinnedFromWire keeps the difference between absent or null (nil: the type default) and [] (pin nothing).
func pinnedFromWire(keys []string) *PinnedFields {
	if keys == nil {
		return nil
	}
	p := PinnedFields(keys)
	return &p
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
