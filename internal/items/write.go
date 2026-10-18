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
	maxTitleLen = 300
	maxURLLen   = 2048
)

// CreateInput is a new item. Its kind is the kind of the list; nil Metadata is {}.
type CreateInput struct {
	Title    string
	Price    *Price
	Website  *string
	Metadata map[string]any
}

// Patch is a partial item update; absent fields stay as they are. A nil
// Metadata is absent, an empty map replaces the metadata with {}.
type Patch struct {
	Title    *string
	Price    tree.Optional[Price]
	Website  tree.Optional[string]
	Metadata map[string]any
}

// Create appends an item to an active list (404 otherwise).
func (s *Service) Create(ctx context.Context, listID uuid.UUID, in CreateInput) (Item, error) {
	var errs []apperr.FieldError
	title, fe := cleanText("title", in.Title, 1, maxTitleLen)
	errs = appendIf(errs, fe)
	website, errs := optionalWebsite(errs, in.Website)
	if in.Price != nil {
		errs = append(errs, validatePrice(in.Price)...)
	}
	// Field rules first (422), then the list and its kind (404), then the kind's metadata rules.
	if err := validate(errs); err != nil {
		return Item{}, err
	}

	var id uuid.UUID
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if err := s.lockTree(ctx, q); err != nil {
			return err
		}
		list, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: listID, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if in.Price != nil {
			if err := priceKindError(list.Kind); err != nil {
				return err
			}
		}
		meta, err := s.ValidateMetadata(ctx, q, list.Kind, in.Metadata)
		if err != nil {
			return err
		}
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("encode metadata: %w", err)
		}
		next, err := q.NextItemSortOrder(ctx, sqlc.NextItemSortOrderParams{ListID: listID, WorkspaceID: s.workspaceID})
		if err != nil {
			return fmt.Errorf("next item sort order: %w", err)
		}
		priceMin, priceMax, currency := priceColumns(in.Price)
		id, err = q.InsertItem(ctx, sqlc.InsertItemParams{
			WorkspaceID: s.workspaceID, ListID: listID, Title: title,
			PriceMin: priceMin, PriceMax: priceMax, Currency: currency, WebsiteUrl: website,
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
// metadata rules of the list's kind. A patch that changes nothing does not bump the version (D-059).
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int, p Patch) (Item, error) {
	var errs []apperr.FieldError
	if p.Title != nil {
		title, fe := cleanText("title", *p.Title, 1, maxTitleLen)
		p.Title = &title
		errs = appendIf(errs, fe)
	}
	if p.Website.Set && p.Website.Value != nil {
		site, fe := cleanWebsite(*p.Website.Value)
		p.Website.Value = &site
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
		list, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: cur.ListID, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if p.Price.Set && p.Price.Value != nil {
			if err := priceKindError(list.Kind); err != nil {
				return err
			}
		}
		arg, changed, err := s.mergePatch(ctx, q, cur, list.Kind, p)
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

// priceKindError rejects a price on an item of a kind that has none: generic,
// movie and tv_show (F009-B1, subsuming F008-B1; 422). Clearing a price (null)
// never gets here.
func priceKindError(kind string) error {
	if kinds.HasPrice(kind) {
		return nil
	}
	return apperr.Validation(apperr.FieldError{Field: "price", Message: "items of this kind have no price"})
}

// mergePatch applies a validated patch to the locked row. It returns the full
// replacement values and whether anything differs from the stored row. kind is
// the kind of the item's list, which the metadata is validated against.
func (s *Service) mergePatch(ctx context.Context, q *sqlc.Queries, cur sqlc.Item, kind string, p Patch) (sqlc.UpdateItemParams, bool, error) {
	arg := sqlc.UpdateItemParams{
		Title:    cur.Title,
		PriceMin: cur.PriceMin, PriceMax: cur.PriceMax, Currency: cur.Currency,
		WebsiteUrl: cur.WebsiteUrl, Metadata: cur.Metadata,
	}
	changed := false
	if p.Title != nil && *p.Title != cur.Title {
		arg.Title, changed = *p.Title, true
	}
	if p.Website.Set && !equalPtr(p.Website.Value, cur.WebsiteUrl) {
		arg.WebsiteUrl, changed = p.Website.Value, true
	}
	if p.Price.Set && !samePrice(p.Price.Value, priceFromRow(cur.PriceMin, cur.PriceMax, cur.Currency)) {
		arg.PriceMin, arg.PriceMax, arg.Currency = priceColumns(p.Price.Value)
		changed = true
	}
	if p.Metadata != nil {
		clean, err := s.ValidateMetadata(ctx, q, kind, p.Metadata)
		if err != nil {
			return arg, false, err
		}
		meta, err := json.Marshal(clean)
		if err != nil {
			return arg, false, fmt.Errorf("encode metadata: %w", err)
		}
		if !sameJSON(meta, cur.Metadata) {
			arg.Metadata, changed = meta, true
		}
	}
	return arg, changed, nil
}

// Move appends an item to the end of another active list. Errors in order:
// NotFound, VersionConflict, DestinationUnavailable, KindMismatch (the
// destination list has another kind than the item's current list, F-007).
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
		destList, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: dest, WorkspaceID: s.workspaceID})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.DestinationUnavailable()
			}
			return fmt.Errorf("destination list: %w", err)
		}
		srcList, err := q.GetActiveListForItems(ctx, sqlc.GetActiveListForItemsParams{ID: cur.ListID, WorkspaceID: s.workspaceID})
		if err != nil {
			return notFoundOr(err, "list")
		}
		if destList.Kind != srcList.Kind {
			return apperr.KindMismatch()
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

// ValidateMetadata loads this workspace's field definitions inside the active
// transaction and validates metadata against them. It is also used by the TMDB
// store, whose import write shares the transaction with its provenance row.
func (s *Service) ValidateMetadata(ctx context.Context, q *sqlc.Queries, kind string, in map[string]any) (map[string]any, error) {
	if in == nil {
		in = map[string]any{}
	}
	definition, err := q.GetKindDefinitionByKey(ctx, sqlc.GetKindDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: kind})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Validation(apperr.FieldError{Field: "kind", Message: "is not a known item kind"})
	}
	if err != nil {
		return nil, fmt.Errorf("get kind definition: %w", err)
	}
	fields, err := q.ListKindFields(ctx, sqlc.ListKindFieldsParams{WorkspaceID: s.workspaceID, KindID: definition.ID})
	if err != nil {
		return nil, fmt.Errorf("list kind fields: %w", err)
	}
	definitions := make([]kinds.Field, len(fields))
	for i, field := range fields {
		definitions[i] = kinds.Field{Key: field.Key, Type: field.FieldType, Special: field.IsLocked}
	}
	return kinds.Validate(definitions, in)
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
		return s, &apperr.FieldError{Field: "website", Message: msg}
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
	na, nb := a.normalized(), b.normalized()
	return equalPtr(na.Min, nb.Min) && equalPtr(na.Max, nb.Max) && na.Currency == nb.Currency
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
