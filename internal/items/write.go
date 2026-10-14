package items

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/items/kinds"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

// Field limits of DATA_MODEL §2 (the same rules are CHECK constraints).
const (
	maxTitleLen   = 300
	maxSummaryLen = 1000
	maxStatusLen  = 100
	maxURLLen     = 2048
)

// CreateInput is a new item. An empty Kind is generic; nil Metadata is {}.
type CreateInput struct {
	Title              string
	Kind               string
	Summary            *string
	Status             *string
	Price              *Price
	OfficialWebsiteURL *string
	Metadata           map[string]any
}

// Patch is a partial item update; absent fields stay as they are. A nil
// Metadata is absent, an empty map replaces the metadata with {}.
type Patch struct {
	Title              *string
	Kind               *string
	Summary            tree.Optional[string]
	Status             tree.Optional[string]
	Price              tree.Optional[Price]
	OfficialWebsiteURL tree.Optional[string]
	Metadata           map[string]any
}

// Create appends an item to an active list (404 otherwise).
func (s *Service) Create(ctx context.Context, listID uuid.UUID, in CreateInput) (Item, error) {
	if in.Kind == "" {
		in.Kind = kinds.Generic
	}
	var errs []apperr.FieldError
	title, fe := cleanText("title", in.Title, 1, maxTitleLen)
	errs = appendIf(errs, fe)
	summary, errs := optionalText(errs, "summary", in.Summary, maxSummaryLen)
	status, errs := optionalText(errs, "status", in.Status, maxStatusLen)
	website, errs := optionalWebsite(errs, in.OfficialWebsiteURL)
	if in.Price != nil {
		errs = append(errs, validatePrice(in.Price)...)
	}
	meta, merrs, err := validateMetadata(in.Kind, in.Metadata)
	if err != nil {
		return Item{}, err
	}
	if err := validate(append(errs, merrs...)); err != nil {
		return Item{}, err
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return Item{}, fmt.Errorf("encode metadata: %w", err)
	}

	var id uuid.UUID
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if _, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: listID, WorkspaceID: s.workspaceID}); err != nil {
			return notFoundOr(err, "list")
		}
		next, err := q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: listID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("next item sort order: %w", err)
		}
		priceMin, priceMax, currency := priceColumns(in.Price)
		id, err = q.InsertItem(ctx, sqlc.InsertItemParams{
			WorkspaceID: s.workspaceID, ListID: listID, Title: title, Kind: in.Kind, Summary: summary, Status: status,
			PriceMin: priceMin, PriceMax: priceMax, Currency: currency, OfficialWebsiteUrl: website,
			Metadata: metaJSON, SortOrder: next, Now: s.now(),
		})
		if err != nil {
			return fmt.Errorf("insert item: %w", err)
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return s.get(ctx, id, false)
}

// Update applies a partial update. version is the caller's If-Match version.
// Errors in order: field rules (422), NotFound, VersionConflict, then the
// kind-dependent metadata rules. A patch that changes nothing does not bump the version (D-059).
// Changing kind without metadata resets metadata to {}.
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int, p Patch) (Item, error) {
	var errs []apperr.FieldError
	if p.Title != nil {
		title, fe := cleanText("title", *p.Title, 1, maxTitleLen)
		p.Title = &title
		errs = appendIf(errs, fe)
	}
	if p.Kind != nil && !kinds.Known(*p.Kind) {
		errs = append(errs, apperr.FieldError{Field: "kind", Message: "is not a known item kind"})
	}
	p.Summary, errs = patchText(errs, "summary", p.Summary, maxSummaryLen)
	p.Status, errs = patchText(errs, "status", p.Status, maxStatusLen)
	if p.OfficialWebsiteURL.Set && p.OfficialWebsiteURL.Value != nil {
		site, fe := cleanWebsite(*p.OfficialWebsiteURL.Value)
		p.OfficialWebsiteURL.Value = &site
		errs = appendIf(errs, fe)
	}
	if p.Price.Set && p.Price.Value != nil {
		errs = append(errs, validatePrice(p.Price.Value)...)
	}
	if err := validate(errs); err != nil {
		return Item{}, err
	}

	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, err := q.LockItem(ctx, sqlc.LockItemParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "item")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		arg, changed, err := mergePatch(cur, p)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		arg.Now, arg.ID, arg.WorkspaceID = s.now(), id, s.workspaceID
		if _, err := q.UpdateItem(ctx, arg); err != nil {
			return fmt.Errorf("update item: %w", err)
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return s.get(ctx, id, false)
}

// mergePatch applies a validated patch to the locked row. It returns the full
// replacement values and whether anything differs from the stored row.
func mergePatch(cur sqlc.Item, p Patch) (sqlc.UpdateItemParams, bool, error) {
	arg := sqlc.UpdateItemParams{
		Title: cur.Title, Kind: cur.Kind, Summary: cur.Summary, Status: cur.Status,
		PriceMin: cur.PriceMin, PriceMax: cur.PriceMax, Currency: cur.Currency,
		OfficialWebsiteUrl: cur.OfficialWebsiteUrl, Metadata: cur.Metadata,
	}
	changed := false
	if p.Title != nil && *p.Title != cur.Title {
		arg.Title, changed = *p.Title, true
	}
	if p.Summary.Set && !equalPtr(p.Summary.Value, cur.Summary) {
		arg.Summary, changed = p.Summary.Value, true
	}
	if p.Status.Set && !equalPtr(p.Status.Value, cur.Status) {
		arg.Status, changed = p.Status.Value, true
	}
	if p.OfficialWebsiteURL.Set && !equalPtr(p.OfficialWebsiteURL.Value, cur.OfficialWebsiteUrl) {
		arg.OfficialWebsiteUrl, changed = p.OfficialWebsiteURL.Value, true
	}
	if p.Price.Set && !samePrice(p.Price.Value, priceFromRow(cur.PriceMin, cur.PriceMax, cur.Currency)) {
		arg.PriceMin, arg.PriceMax, arg.Currency = priceColumns(p.Price.Value)
		changed = true
	}

	kind := cur.Kind
	if p.Kind != nil {
		kind = *p.Kind
	}
	if kind != cur.Kind {
		arg.Kind, changed = kind, true
	}
	var meta []byte
	switch {
	case p.Metadata != nil:
		clean, merrs, err := validateMetadata(kind, p.Metadata)
		if err != nil {
			return arg, false, err
		}
		if len(merrs) > 0 {
			return arg, false, apperr.Validation(merrs...)
		}
		if meta, err = json.Marshal(clean); err != nil {
			return arg, false, fmt.Errorf("encode metadata: %w", err)
		}
	case kind != cur.Kind:
		meta = []byte("{}")
	}
	if meta != nil && !sameJSON(meta, cur.Metadata) {
		arg.Metadata, changed = meta, true
	}
	return arg, changed, nil
}

// Move appends an item to the end of another active list. Errors in order:
// NotFound, VersionConflict, DestinationUnavailable.
func (s *Service) Move(ctx context.Context, id uuid.UUID, version int, dest uuid.UUID) (Item, error) {
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		cur, err := q.LockItem(ctx, sqlc.LockItemParams{ID: id, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "item")
		}
		if int(cur.Version) != version {
			return apperr.VersionConflict(int(cur.Version))
		}
		if _, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: dest, WorkspaceID: s.workspaceID}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.DestinationUnavailable()
			}
			return fmt.Errorf("destination list: %w", err)
		}
		next, err := q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: dest, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("next item sort order: %w", err)
		}
		if _, err := q.MoveItem(ctx, sqlc.MoveItemParams{ListID: dest, SortOrder: next, Now: s.now(), ID: id, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("move item: %w", err)
		}
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return s.get(ctx, id, false)
}

// Reorder rewrites the order of an active list's items. ids must be exactly
// the active items of the list, else OrderStale. It works in any sort_mode and
// does not bump item versions (DATA_MODEL §7).
func (s *Service) Reorder(ctx context.Context, listID uuid.UUID, ids []uuid.UUID) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		if _, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: listID, WorkspaceID: s.workspaceID}); err != nil {
			return notFoundOr(err, "list")
		}
		current, err := q.ListActiveItemIDsByList(ctx, sqlc.ListActiveItemIDsByListParams{ListID: listID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("list item ids: %w", err)
		}
		if !sameSet(current, ids) {
			return apperr.OrderStale()
		}
		if err := q.ReorderItems(ctx, sqlc.ReorderItemsParams{Now: s.now(), WorkspaceID: s.workspaceID, ListID: listID, Ids: ids}); err != nil {
			return fmt.Errorf("reorder items: %w", err)
		}
		return nil
	})
}

// validateMetadata runs the kind validator. An undeclared key comes back as
// the error (a 400); field rule failures come back as field errors so they
// merge with the other 422 errors of the request.
func validateMetadata(kind string, in map[string]any) (map[string]any, []apperr.FieldError, error) {
	if in == nil {
		in = map[string]any{}
	}
	out, err := kinds.Validate(kind, in)
	var invalid *apperr.ValidationError
	switch {
	case err == nil:
		return out, nil, nil
	case errors.As(err, &invalid):
		return nil, invalid.Errors, nil
	default:
		return nil, nil, err
	}
}

func validate(errs []apperr.FieldError) error {
	if len(errs) == 0 {
		return nil
	}
	return apperr.Validation(errs...)
}

func appendIf(errs []apperr.FieldError, fe *apperr.FieldError) []apperr.FieldError {
	if fe != nil {
		return append(errs, *fe)
	}
	return errs
}

// cleanText trims s and checks its length in characters.
func cleanText(field, s string, minLen, maxLen int) (string, *apperr.FieldError) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < minLen || n > maxLen {
		msg := fmt.Sprintf("must be at most %d characters", maxLen)
		if minLen > 0 {
			msg = fmt.Sprintf("must be %d to %d characters", minLen, maxLen)
		}
		return s, &apperr.FieldError{Field: field, Message: msg}
	}
	return s, nil
}

func optionalText(errs []apperr.FieldError, field string, in *string, maxLen int) (*string, []apperr.FieldError) {
	if in == nil {
		return nil, errs
	}
	s, fe := cleanText(field, *in, 0, maxLen)
	return &s, appendIf(errs, fe)
}

func patchText(errs []apperr.FieldError, field string, in tree.Optional[string], maxLen int) (tree.Optional[string], []apperr.FieldError) {
	if in.Set && in.Value != nil {
		in.Value, errs = optionalText(errs, field, in.Value, maxLen)
	}
	return in, errs
}

func optionalWebsite(errs []apperr.FieldError, in *string) (*string, []apperr.FieldError) {
	if in == nil {
		return nil, errs
	}
	s, fe := cleanWebsite(*in)
	return &s, appendIf(errs, fe)
}

// cleanWebsite accepts absolute http and https URLs only (`javascript:` and
// friends are rejected) of at most 2048 characters.
func cleanWebsite(s string) (string, *apperr.FieldError) {
	s = strings.TrimSpace(s)
	bad := func(msg string) (string, *apperr.FieldError) {
		return s, &apperr.FieldError{Field: "official_website_url", Message: msg}
	}
	if utf8.RuneCountInString(s) > maxURLLen {
		return bad(fmt.Sprintf("must be at most %d characters", maxURLLen))
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return bad("must be an http or https URL")
	}
	return s, nil
}

func equalPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func samePrice(a, b *Price) bool {
	if a == nil || b == nil {
		return a == b
	}
	return equalPtr(a.Min, b.Min) && equalPtr(a.Max, b.Max) && a.Currency == b.Currency
}

// sameJSON compares two JSON documents by value (key order, spacing and
// number formatting do not matter).
func sameJSON(a, b []byte) bool {
	canon := func(raw []byte) []byte {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return raw
		}
		out, err := json.Marshal(v)
		if err != nil {
			return raw
		}
		return out
	}
	return bytes.Equal(canon(a), canon(b))
}
