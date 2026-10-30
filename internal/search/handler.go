package search

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Register adds the search operations to the API. The handlers do HTTP only:
// parse the input, call the service, shape the output. Errors go back as they
// are: httpserver.Op maps them to problems.
func Register(api huma.API, log *slog.Logger, svc *Service) {
	h := &handler{svc: svc}
	tags := []string{"search"}

	huma.Register(api, huma.Operation{
		OperationID: "searchItems", Method: http.MethodGet, Path: "/api/v1/search", Tags: tags,
		Summary:     "Search items by title",
		Description: "Case-insensitive substring match. Exact matches first, then alphabetical.",
		Errors:      []int{http.StatusBadRequest},
	}, httpserver.Op(log, h.search))
	huma.Register(api, huma.Operation{
		OperationID: "listRecentItems", Method: http.MethodGet, Path: "/api/v1/recent-items", Tags: tags,
		Summary: "List recently updated items",
		Description: "Active items across the workspace, most recently updated first (a PATCH or a content save moves an item up), with their paths.\n" +
			"Not paginated: it feeds the Home block.",
		Errors: []int{http.StatusBadRequest},
	}, httpserver.Op(log, h.recent))
}

type handler struct{ svc *Service }

// rejectEmpty answers 400 for a present but empty query parameter. Huma reads an
// empty value as absent, so the old rule ("present and empty is invalid") is kept here.
func rejectEmpty(ctx huma.Context, names ...string) []error {
	u := ctx.URL()
	query := u.Query()
	var errs []error
	for _, name := range names {
		if vs, ok := query[name]; ok && len(vs) > 0 && vs[0] == "" {
			errs = append(errs, &huma.ErrorDetail{Location: "query." + name, Message: "must not be empty"})
		}
	}
	return errs
}

// SearchInput is the searchItems query. q is checked by the service after trimming,
// so a blank q answers the same 400 as before.
type SearchInput struct {
	Q      string `query:"q" required:"true" minLength:"1" maxLength:"200" doc:"Case-insensitive title substring, 1 to 200 characters."`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
	Offset int    `query:"offset" default:"0" minimum:"0" doc:"Number of rows to skip."`
}

// Resolve answers 400 for a present but empty limit or offset.
func (in *SearchInput) Resolve(ctx huma.Context) []error {
	return rejectEmpty(ctx, "limit", "offset")
}

// SearchOutput is the searchItems answer.
type SearchOutput struct {
	Body SearchPageJSON
}

func (h *handler) search(ctx context.Context, in *SearchInput) (*SearchOutput, error) {
	page, err := h.svc.Search(ctx, Params{Q: in.Q, Limit: in.Limit, Offset: in.Offset})
	if errors.Is(err, ErrInvalidQuery) {
		// The schema cannot see a q that is only whitespace.
		return nil, problem.New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter",
			`"q" must be 1 to 200 characters after trimming.`)
	}
	if err != nil {
		return nil, err
	}
	out := SearchPageJSON{Data: make([]SearchResultJSON, 0, len(page.Results)), Page: items.PageJSON{Limit: page.Limit, Offset: page.Offset, Total: page.Total}}
	for _, r := range page.Results {
		out.Data = append(out.Data, resultJSON(r))
	}
	return &SearchOutput{Body: out}, nil
}

// RecentInput is the listRecentItems query.
type RecentInput struct {
	Limit int `query:"limit" default:"12" minimum:"1" maximum:"50" doc:"Number of items to return."`
}

// Resolve answers 400 for a present but empty limit.
func (in *RecentInput) Resolve(ctx huma.Context) []error {
	return rejectEmpty(ctx, "limit")
}

// RecentOutput is the listRecentItems answer.
type RecentOutput struct {
	Body RecentItemsJSON
}

func (h *handler) recent(ctx context.Context, in *RecentInput) (*RecentOutput, error) {
	results, err := h.svc.Recent(ctx, in.Limit)
	if err != nil {
		return nil, err
	}
	out := RecentItemsJSON{Data: make([]SearchResultJSON, 0, len(results))}
	for _, r := range results {
		out.Data = append(out.Data, resultJSON(r))
	}
	return &RecentOutput{Body: out}, nil
}
