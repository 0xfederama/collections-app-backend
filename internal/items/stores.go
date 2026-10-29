package items

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

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
