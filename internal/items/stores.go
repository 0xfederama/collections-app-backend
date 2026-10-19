package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/stores"
)

// storeLinks builds the read-only store_links of a single item (F-016, D-096): for
// each provider key selected in a stores field of the item's type, the URL built
// from the item's title and its isbn and openlibrary_id metadata. Nothing is
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
	var meta map[string]any
	if len(it.Metadata) > 0 {
		if err := json.Unmarshal(it.Metadata, &meta); err != nil {
			return nil, fmt.Errorf("decode item metadata: %w", err)
		}
	}
	in := stores.LinkInput{Title: it.Title}
	// Links follow the workspace country, so a later change of the setting moves them.
	settings, err := q.GetSettings(ctx, s.workspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	if err == nil {
		in.Country = settings.StreamingCountryCode
	}
	in.ISBN, _ = meta["isbn"].(string)
	in.OpenLibraryID, _ = meta["openlibrary_id"].(string)

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

// applyPreferredStores fills every stores field the create request left unset
// (absent or null) with the workspace's preferred stores (F-016, D-096). An
// explicit value, including [], wins. With no preferred stores nothing is added.
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
	if len(settings.PreferredStores) == 0 {
		return meta, nil
	}
	out := make(map[string]any, len(meta)+len(unset))
	for k, v := range meta {
		out[k] = v
	}
	for _, key := range unset {
		out[key] = append([]string{}, settings.PreferredStores...)
	}
	return out, nil
}
