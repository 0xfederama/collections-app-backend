package items

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/etag"
	"github.com/0xfederama/collections-app-backend/internal/platform/httpserver"
	"github.com/0xfederama/collections-app-backend/internal/platform/problem"
)

// Importer creates an item through the lookup providers (movie, TV and book
// lists). It is the create path when lookup is configured: the composition root
// wires it over internal/lookup, which keeps the plain create for the other
// types. Nil means no lookup, and the create goes straight to the service.
type Importer interface {
	Create(ctx context.Context, listID uuid.UUID, in CreateInput, opts ImportOptions) (Item, error)
}

// ImportOptions are the lookup fields of a create request. Lookup nil means the
// default (on for the types that have a provider).
type ImportOptions struct {
	Lookup   *bool
	TMDBID   *int
	SourceID *string
}

// Register adds the item operations to the API. The handlers do HTTP only: parse
// the input, call the service, shape the output. Errors go back as they are:
// httpserver.Op maps them to problems. The lookup operations and the image,
// content and delete operations are not here yet.
func Register(api huma.API, log *slog.Logger, svc *Service, lk Importer) {
	h := &handler{svc: svc, lookup: lk}
	tags := []string{"items"}

	huma.Register(api, huma.Operation{
		OperationID: "listItems", Method: http.MethodGet, Path: "/api/v1/lists/{id}/items", Tags: tags,
		Summary: "List the items of a list",
		Description: "Sorted by `sort` (default: the list's `sort_mode`), tie-break `id`. `q` filters by title and, on movie lists, director. " +
			"`price` sorts by `price.min` and is available only when the type allows price. Field filters and `availability` combine with `q` and each other.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.list))
	huma.Register(api, huma.Operation{
		OperationID: "createItem", Method: http.MethodPost, Path: "/api/v1/lists/{id}/items", Tags: tags,
		Summary: "Create an item", DefaultStatus: http.StatusCreated,
		Description: "Appended to the end of the list. The item's `type` is the list's type (read-only, not accepted in the body). " +
			"`metadata` is validated against the list's type. A `price` on a type without a price is `422 validation_failed`. " +
			"A title already used in the list (trimmed, ignoring case and accents) is `409 duplicate_title`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity, http.StatusServiceUnavailable},
	}, httpserver.Op(log, h.create))
	huma.Register(api, huma.Operation{
		OperationID: "getItem", Method: http.MethodGet, Path: "/api/v1/items/{id}", Tags: tags,
		Summary: "Get an item", Description: "Includes `path` (the ancestor folders and the list).",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound},
	}, httpserver.Op(log, h.get))
	huma.Register(api, huma.Operation{
		OperationID: "updateItem", Method: http.MethodPatch, Path: "/api/v1/items/{id}", Tags: tags,
		Summary: "Update an item",
		Description: "Requires `If-Match` (`\"i-N\"`). Any of `title, price, website, metadata`; send `null` to clear a nullable field. " +
			"`metadata` is validated against the list's type and replaces the stored one. The type cannot be changed here. " +
			"A new title already used by another item of the list is `409 duplicate_title`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusPreconditionFailed,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity,
			http.StatusPreconditionRequired},
		Middlewares: huma.Middlewares{httpserver.IfMatchFirst(api, etag.Item)},
	}, httpserver.Op(log, h.update))
	huma.Register(api, huma.Operation{
		OperationID: "moveItem", Method: http.MethodPost, Path: "/api/v1/items/{id}/move", Tags: tags,
		Summary: "Move an item to another list",
		Description: "Requires `If-Match` (`\"i-N\"`). Appended to the end of the destination list. A missing or trashed list returns " +
			"`409 destination_unavailable`. A destination list of another type returns `409 type_mismatch`. " +
			"A destination list that already has the item's title returns `409 duplicate_title`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusPreconditionFailed, http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity, http.StatusPreconditionRequired},
		Middlewares: huma.Middlewares{httpserver.IfMatchFirst(api, etag.Item)},
	}, httpserver.Op(log, h.move))
	huma.Register(api, huma.Operation{
		OperationID: "reorderListItems", Method: http.MethodPut, Path: "/api/v1/lists/{id}/items/order", Tags: tags,
		Summary: "Reorder the items of a list", DefaultStatus: http.StatusNoContent,
		Description: "`ids` must be all active items of the list, in the new order. Allowed regardless of `sort_mode`. No `If-Match`.",
		Errors: []int{http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity},
	}, httpserver.Op(log, h.reorder))
}

type handler struct {
	svc    *Service
	lookup Importer
}

// IDParam is the {id} path parameter.
type IDParam struct {
	ID uuid.UUID `path:"id" format:"uuid"`
}

// ItemOutput is an item with its ETag.
type ItemOutput struct {
	ETag string `header:"ETag"`
	Body ItemJSON
}

// CreatedItemOutput adds the Location of the new item.
type CreatedItemOutput struct {
	ETag     string `header:"ETag"`
	Location string `header:"Location"`
	Body     ItemJSON
}

// ItemPageOutput is the listItems answer.
type ItemPageOutput struct {
	Body ItemPageJSON
}

// ListItemsInput is the listItems path and query. Huma cannot take pointers for
// query parameters, and it reads an empty value as absent: Resolve keeps the
// old rule that a present but empty parameter is a 400.
type ListItemsInput struct {
	IDParam
	Sort         string `query:"sort" minLength:"1" maxLength:"32" doc:"Defaults to the list's sort_mode; may be a field key of the list type."`
	Q            string `query:"q" minLength:"1" maxLength:"200" doc:"Case-insensitive title filter; on movie lists it also matches metadata.director."`
	FilterField  string `query:"filter_field" minLength:"1" maxLength:"32" doc:"Type field key to filter. Requires value or range parameters matching the field type."`
	FilterValue  string `query:"filter_value" maxLength:"200" doc:"Text/link substring or boolean (true/false) filter value."`
	FilterMin    string `query:"filter_min" maxLength:"64" doc:"Inclusive lower bound for number or ISO date field filters."`
	FilterMax    string `query:"filter_max" maxLength:"64" doc:"Inclusive upper bound for number or ISO date field filters."`
	Availability string `query:"availability" enum:"available,included" doc:"Movie and TV lists only. available: at least one streaming platform on an owned service (any service when settings.owned_streaming_services is empty). included: the same, counting only platforms not paid per title (subscription, free or ads; no rent or buy)."`
	Limit        int    `query:"limit" default:"50" minimum:"1" maximum:"200" doc:"Page size."`
	Offset       int    `query:"offset" default:"0" minimum:"0" doc:"Number of rows to skip."`
}

// emptyRejected are the query parameters the old spec rejected when present and empty.
var emptyRejected = []string{"sort", "q", "filter_field", "limit", "offset"}

// Resolve answers 400 invalid_parameter for an empty value of a parameter that
// has a length or range rule (the problem mapping names the parameter).
func (in *ListItemsInput) Resolve(ctx huma.Context) []error {
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

func (h *handler) list(ctx context.Context, in *ListItemsInput) (*ItemPageOutput, error) {
	page, err := h.svc.List(ctx, in.ID, ListParams{
		Sort: in.Sort, Q: in.Q, FilterField: in.FilterField, FilterValue: in.FilterValue,
		FilterMin: in.FilterMin, FilterMax: in.FilterMax, Availability: in.Availability, Limit: in.Limit, Offset: in.Offset,
	})
	if err != nil {
		return nil, queryProblem(err)
	}
	out := ItemPageJSON{Data: make([]ItemSummaryJSON, 0, len(page.Items)), Page: pageOf(page)}
	for _, it := range page.Items {
		out.Data = append(out.Data, ToItemSummaryJSON(it))
	}
	return &ItemPageOutput{Body: out}, nil
}

// queryProblem answers a listItems query the list's type cannot serve with the
// 400 invalid_parameter of the old handler, which names what is wrong.
func queryProblem(err error) error {
	switch {
	case errors.Is(err, ErrInvalidSort):
		return problem.New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter",
			"sort must be manual, title, updated, price (when allowed), or a field key of this list's type.")
	case errors.Is(err, ErrInvalidAvailability):
		return problem.New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter",
			"availability is only available on movie and TV lists.")
	case errors.Is(err, ErrInvalidFilter):
		return problem.New(http.StatusBadRequest, "invalid_parameter", "Invalid parameter",
			"field filter parameters must match a sortable field's type.")
	}
	return err
}

// CreateItemInput is the createItem path and body.
type CreateItemInput struct {
	IDParam
	Body CreateItemRequest
}

func (h *handler) create(ctx context.Context, in *CreateItemInput) (*CreatedItemOutput, error) {
	b := in.Body
	ci := CreateInput{Title: b.Title, Price: priceInput(b.Price), Website: b.Website, Metadata: b.Metadata}
	var it Item
	var err error
	if h.lookup != nil {
		it, err = h.lookup.Create(ctx, in.ID, ci, ImportOptions{Lookup: b.Lookup, TMDBID: b.TmdbID, SourceID: b.SourceID})
	} else {
		it, err = h.svc.Create(ctx, in.ID, ci)
	}
	if err != nil {
		return nil, err
	}
	body, err := ToItemJSON(it)
	if err != nil {
		return nil, err
	}
	return &CreatedItemOutput{ETag: itemETag(it), Location: "/api/v1/items/" + it.ID.String(), Body: body}, nil
}

func (h *handler) get(ctx context.Context, in *IDParam) (*ItemOutput, error) {
	it, err := h.svc.Get(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return itemOutput(it)
}

// UpdateItemInput is the updateItem path, If-Match and body.
type UpdateItemInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
	Body    UpdateItemRequest
}

func (h *handler) update(ctx context.Context, in *UpdateItemInput) (*ItemOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	b := in.Body
	it, err := h.svc.Update(ctx, in.ID, version, Patch{
		Title: b.Title, Price: priceOptional(b.Price), Website: websiteOptional(b.Website), Metadata: b.Metadata,
	})
	if err != nil {
		return nil, err
	}
	return itemOutput(it)
}

// MoveItemInput is the moveItem path, If-Match and body.
type MoveItemInput struct {
	IDParam
	IfMatch string `header:"If-Match" doc:"Item ETag, \"i-N\"."`
	Body    MoveItemRequest
}

func (h *handler) move(ctx context.Context, in *MoveItemInput) (*ItemOutput, error) {
	version, ok := etag.Parse(in.IfMatch, etag.Item)
	if !ok {
		return nil, problem.PreconditionRequired()
	}
	if in.Body.ListID == uuid.Nil {
		return nil, apperr.Validation(apperr.FieldError{Field: "list_id", Message: "is required"})
	}
	it, err := h.svc.Move(ctx, in.ID, version, in.Body.ListID)
	if err != nil {
		return nil, err
	}
	return itemOutput(it)
}

// ReorderItemsInput is the reorderListItems path and body.
type ReorderItemsInput struct {
	IDParam
	Body ReorderItemsRequest
}

func (h *handler) reorder(ctx context.Context, in *ReorderItemsInput) (*struct{}, error) {
	if err := h.svc.Reorder(ctx, in.ID, in.Body.IDs); err != nil {
		return nil, err
	}
	return nil, nil
}

// itemOutput is an item with its ETag.
func itemOutput(it Item) (*ItemOutput, error) {
	body, err := ToItemJSON(it)
	if err != nil {
		return nil, err
	}
	return &ItemOutput{ETag: itemETag(it), Body: body}, nil
}

func itemETag(it Item) string { return etag.Format(etag.Item, it.Version) }

// priceInput converts a wire price to the model; nil stays nil.
func priceInput(p *PriceInput) *Price {
	if p == nil {
		return nil
	}
	return &Price{Amount: p.Amount, Min: p.Min, Max: p.Max, Currency: p.Currency}
}

// priceOptional converts the wire PATCH price: absent is not Set, null is Set
// with no value (clear the price).
func priceOptional(o httpserver.Optional[PriceInput]) Optional[Price] {
	return Optional[Price]{Set: o.Set, Value: priceInput(o.Value)}
}

// websiteOptional converts the wire PATCH website: absent is not Set, null clears it.
func websiteOptional(o httpserver.Optional[string]) Optional[string] {
	return Optional[string]{Set: o.Set, Value: o.Value}
}
