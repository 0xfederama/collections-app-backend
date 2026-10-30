package content

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

// contentBodyBytes is the JSON limit of the save and restore operations (API.md
// §6). The 1 MiB text rule is the service's (413 above it), counted in bytes.
const contentBodyBytes = 2 << 20

// Register adds the content operations to the API. The handlers do HTTP only:
// parse the input, call the service, shape the output. Errors go back as they
// are: httpserver.Op maps them to problems.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"content"}

	huma.Register(api, huma.Operation{
		OperationID: "getItemContent", Method: http.MethodGet, Path: "/api/v1/items/{id}/content", Tags: tags,
		Summary: "Get the current Markdown content", Description: "Version 0 with an empty body if the item was never saved.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.get))
	huma.Register(api, huma.Operation{
		OperationID: "updateItemContent", Method: http.MethodPut, Path: "/api/v1/items/{id}/content", Tags: tags,
		Summary: "Save the Markdown content",
		Description: "Requires `If-Match` (`\"c-N\"`, the current content version). The body is at most 1 MiB UTF-8. " +
			"An identical body returns 200 with the same version. Bumps the item's `updated_at` but not its `version`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
		MaxBodyBytes: contentBodyBytes,
		Middlewares:  huma.Middlewares{httpserver.IfMatchFirst(api, etag.Content)},
	}, httpserver.Op(log, h.update))
	huma.Register(api, huma.Operation{
		OperationID: "listContentVersions", Method: http.MethodGet, Path: "/api/v1/items/{id}/content/versions", Tags: tags,
		Summary: "List the saved content versions", Description: "Newest first, unpaginated (at most 201).",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.versions))
	huma.Register(api, huma.Operation{
		OperationID: "getContentVersion", Method: http.MethodGet, Path: "/api/v1/items/{id}/content/versions/{version}", Tags: tags,
		Summary:     "Get one content version (for preview)",
		Description: "Returns 404 if that version never existed or was pruned. The `ETag` is that version's (`\"c-N\"`).",
		Errors:      []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.version))
	huma.Register(api, huma.Operation{
		OperationID: "restoreContentVersion", Method: http.MethodPost, Path: "/api/v1/items/{id}/content/restore", Tags: tags,
		Summary: "Restore an older content version",
		Description: "Requires `If-Match` (`\"c-N\"`, the **current** version). Creates a new version with the body of `version` and returns it. " +
			"404 if that version was pruned.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
		MaxBodyBytes: contentBodyBytes,
		Middlewares:  huma.Middlewares{httpserver.IfMatchFirst(api, etag.Content)},
	}, httpserver.Op(log, h.restore))
}

type handler struct{ svc *Service }

// ItemIDParam is the {id} path parameter of the content operations.
type ItemIDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

// ContentOutput is a content version with its ETag.
type ContentOutput struct {
	ETag string `header:"ETag"`
	Body ContentJSON
}

// ContentVersionListOutput is the listContentVersions answer.
type ContentVersionListOutput struct {
	Body ContentVersionListJSON
}

// ContentVersionInput is the getContentVersion path.
type ContentVersionInput struct {
	ItemIDParam
	Version int `path:"version" doc:"Content version number."`
}

// UpdateContentInput is the updateItemContent path, If-Match and body.
type UpdateContentInput struct {
	ItemIDParam
	IfMatch string `header:"If-Match" doc:"Content ETag, \"c-N\"."`
	Body    UpdateContentRequest
}

// RestoreContentInput is the restoreContentVersion path, If-Match and body.
type RestoreContentInput struct {
	ItemIDParam
	IfMatch string `header:"If-Match" doc:"Content ETag of the current version, \"c-N\"."`
	Body    RestoreContentRequest
}

func (h *handler) get(ctx context.Context, in *ItemIDParam) (*ContentOutput, error) {
	c, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return contentOutput(c), nil
}

func (h *handler) versions(ctx context.Context, in *ItemIDParam) (*ContentVersionListOutput, error) {
	vs, err := h.svc.Versions(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &ContentVersionListOutput{Body: versionListJSON(vs)}, nil
}

func (h *handler) version(ctx context.Context, in *ContentVersionInput) (*ContentOutput, error) {
	c, err := h.svc.Version(ctx, in.ID, in.Version)
	if err != nil {
		return nil, err
	}
	return contentOutput(c), nil
}

func (h *handler) update(ctx context.Context, in *UpdateContentInput) (*ContentOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Content)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	c, err := h.svc.Save(ctx, in.ID, version, in.Body.Body)
	if err != nil {
		return nil, err
	}
	return contentOutput(c), nil
}

func (h *handler) restore(ctx context.Context, in *RestoreContentInput) (*ContentOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Content)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	c, err := h.svc.Restore(ctx, in.ID, version, in.Body.Version)
	if err != nil {
		return nil, err
	}
	return contentOutput(c), nil
}

// contentOutput is a content version with its ETag.
func contentOutput(c Content) *ContentOutput {
	return &ContentOutput{ETag: etag.Format(etag.Content, c.Version), Body: contentJSON(c)}
}
