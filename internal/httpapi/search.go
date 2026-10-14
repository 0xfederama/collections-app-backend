package httpapi

import (
	"context"
	"errors"

	"github.com/0xfederama/collections-app-backend/internal/httpapi/oapi"
	"github.com/0xfederama/collections-app-backend/internal/search"
)

func (s *Server) SearchItems(ctx context.Context, r oapi.SearchItemsRequestObject) (oapi.SearchItemsResponseObject, error) {
	p := search.Params{Q: r.Params.Q}
	if r.Params.Limit != nil {
		p.Limit = *r.Params.Limit
	}
	if r.Params.Offset != nil {
		p.Offset = *r.Params.Offset
	}
	page, err := s.deps.Search.Search(ctx, p)
	if errors.Is(err, search.ErrInvalidQuery) {
		// The guard already rejects an empty or too long q; this is q that is only whitespace.
		pr := invalidParameter(`"q" must be 1 to 200 characters after trimming.`)
		return oapi.SearchItems400ApplicationProblemPlusJSONResponse{
			BadRequestApplicationProblemPlusJSONResponse: oapi.BadRequestApplicationProblemPlusJSONResponse{
				Type: pr.Type, Title: pr.Title, Status: pr.Status, Code: oapi.ProblemCode(pr.Code), Detail: &pr.Detail,
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	out := oapi.SearchItems200JSONResponse{
		Data: make([]oapi.SearchResult, 0, len(page.Results)),
		Page: oapi.Page{Limit: page.Limit, Offset: page.Offset, Total: page.Total},
	}
	for _, res := range page.Results {
		out.Data = append(out.Data, searchResultBody(res))
	}
	return out, nil
}

func (s *Server) ListRecentItems(ctx context.Context, r oapi.ListRecentItemsRequestObject) (oapi.ListRecentItemsResponseObject, error) {
	limit := 0
	if r.Params.Limit != nil {
		limit = *r.Params.Limit
	}
	results, err := s.deps.Search.Recent(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := oapi.ListRecentItems200JSONResponse{Data: make([]oapi.SearchResult, 0, len(results))}
	for _, res := range results {
		out.Data = append(out.Data, searchResultBody(res))
	}
	return out, nil
}

// searchResultBody maps a service result (item and path) to the shared SearchResult body.
func searchResultBody(res search.Result) oapi.SearchResult {
	path := make([]oapi.PathSegment, 0, len(res.Path))
	for _, seg := range res.Path {
		path = append(path, oapi.PathSegment{Type: oapi.PathSegmentType(seg.Type), Id: seg.ID, Name: seg.Name})
	}
	return oapi.SearchResult{Item: itemSummaryBody(res.Item), Path: path}
}
