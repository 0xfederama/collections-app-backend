package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/stores"
)

// storeLinks builds the read-only store_links of a single item (F-016, D-096): for
// each provider key selected in a stores field of the item's type, the URL built
// from the item's title and its isbn metadata. Nothing is
// stored. It returns nil when the type has no stores field, and an empty map
// when the field selects nothing.
func (s *Service) storeLinks(ctx context.Context, q *sqlc.Queries, it Item) (map[string]string, error) {
	if it.Type == nil {
		return nil, nil
	}
	definition, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: *it.Type})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get type definition: %w", err)
	}
	fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: definition.ID})
	if err != nil {
		return nil, fmt.Errorf("list type fields: %w", err)
	}
	var storeKeys []string
	for _, f := range fields {
		if f.FieldType == "stores" {
			storeKeys = append(storeKeys, f.Key)
		}
	}
	if storeKeys == nil {
		return nil, nil
	}
	// Links follow the workspace country, so a later change of the setting moves them.
	country := ""
	settings, err := q.GetSettings(ctx, s.workspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	if err == nil {
		country = settings.StreamingCountryCode
	}
	return buildStoreLinks(it, storeKeys, country)
}

// buildStoreLinks computes the links of one item from its stores field keys.
func buildStoreLinks(it Item, storeKeys []string, country string) (map[string]string, error) {
	var meta map[string]any
	if len(it.Metadata) > 0 {
		if err := json.Unmarshal(it.Metadata, &meta); err != nil {
			return nil, fmt.Errorf("decode item metadata: %w", err)
		}
	}
	in := stores.LinkInput{Title: it.Title, Country: country}
	in.ISBN, _ = meta["isbn"].(string)

	links := map[string]string{}
	for _, key := range storeKeys {
		selected, _ := meta[key].([]any)
		for _, v := range selected {
			provider, ok := v.(string)
			if !ok || !stores.Valid(provider) {
				continue
			}
			if link := stores.Link(provider, in); link != "" {
				links[provider] = link
			}
		}
	}
	return links, nil
}

// FillStoreLinks sets StoreLinks on every item of a page (rows of a list, search
// or recent items) with a fixed number of queries: the stores fields of all types
// and the workspace settings, each read once, and only when a typed item is
// present. Items whose type has no stores field keep nil.
func FillStoreLinks(ctx context.Context, db sqlc.DBTX, workspaceID uuid.UUID, page []*Item) error {
	typed := false
	for _, it := range page {
		if it.Type != nil {
			typed = true
			break
		}
	}
	if !typed {
		return nil
	}
	q := sqlc.New(db)
	rows, err := q.ListStoresFieldKeys(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list stores fields: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	byType := map[string][]string{}
	for _, r := range rows {
		byType[r.TypeKey] = append(byType[r.TypeKey], r.FieldKey)
	}
	country := ""
	settings, err := q.GetSettings(ctx, workspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("get settings: %w", err)
	}
	if err == nil {
		country = settings.StreamingCountryCode
	}
	for _, it := range page {
		if it.Type == nil || byType[*it.Type] == nil {
			continue
		}
		if it.StoreLinks, err = buildStoreLinks(*it, byType[*it.Type], country); err != nil {
			return err
		}
	}
	return nil
}

// applyPreferredStores fills every stores field the create request left unset
// (absent or null) with the workspace's default store, [preferred_store] (F-016,
// D-096). An explicit value, including [], wins.
// meta is the validated metadata; the result is meta or a copy with the defaults.
func (s *Service) applyPreferredStores(ctx context.Context, q *sqlc.Queries, typ *string, requested, meta map[string]any) (map[string]any, error) {
	if typ == nil {
		return meta, nil
	}
	definition, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: *typ})
	if err != nil {
		return nil, fmt.Errorf("get type definition: %w", err)
	}
	fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: definition.ID})
	if err != nil {
		return nil, fmt.Errorf("list type fields: %w", err)
	}
	var unset []string
	for _, f := range fields {
		if _, set := meta[f.Key]; f.FieldType == "stores" && !set && requested[f.Key] == nil {
			unset = append(unset, f.Key)
		}
	}
	if len(unset) == 0 {
		return meta, nil
	}
	settings, err := q.GetSettings(ctx, s.workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return meta, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	out := make(map[string]any, len(meta)+len(unset))
	for k, v := range meta {
		out[k] = v
	}
	for _, key := range unset {
		out[key] = []string{settings.PreferredStore}
	}
	return out, nil
}
