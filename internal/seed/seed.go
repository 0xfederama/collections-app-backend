// Package seed writes the built-in types of a workspace (DATA_MODEL §2, F-018).
// The data is Go, not SQL: BuiltinTypes runs at bootstrap on every start.
package seed

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// builtinField is one locked field of a built-in type. Its position is its index.
type builtinField struct {
	key       string
	label     string
	fieldType string
}

// builtinType is one locked built-in type. A nil icon or sort field is NULL.
type builtinType struct {
	key         string
	name        string
	icon        *string
	allowsPrice bool
	sortField   *string
	pins        string
	fields      []builtinField
}

// builtins is the built-in set (exactly these five, F-018). Existing rows are
// never overwritten: the pins are only set while NULL.
var builtins = []builtinType{
	{
		key: "movie", name: "Movies", allowsPrice: false, sortField: ptr("release_year"),
		pins: `["runtime_minutes","streaming"]`,
		fields: []builtinField{
			{"release_year", "Release year", "number"},
			{"runtime_minutes", "Runtime", "number"},
			{"director", "Director", "text"},
			{"imdb_url", "IMDb URL", "link"},
			{"streaming", "Streaming", "streaming"},
			{"tmdb_id", "TMDB ID", "number"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		key: "tv_show", name: "TV shows", allowsPrice: false, sortField: ptr("first_air_year"),
		pins: `["seasons","streaming"]`,
		fields: []builtinField{
			{"first_air_year", "First air year", "number"},
			{"end_year", "End year", "number"},
			{"seasons", "Seasons", "number"},
			{"episodes_per_season", "Episodes per season", "number"},
			{"episode_minutes", "Episode minutes", "number"},
			{"imdb_url", "IMDb URL", "link"},
			{"streaming", "Streaming", "streaming"},
			{"tmdb_id", "TMDB ID", "number"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		key: "book", name: "Books", allowsPrice: false,
		pins: `["author","pages","stores"]`,
		fields: []builtinField{
			{"author", "Writer", "text"},
			{"pages", "Pages", "number"},
			{"isbn", "ISBN", "text"},
			{"stores", "Where to get it", "stores"},
			{"openlibrary_id", "Open Library ID", "text"},
			{"lookup_disabled", "Disable lookup", "boolean"},
		},
	},
	{
		key: "vehicle", name: "Vehicles", icon: ptr("i:car"), allowsPrice: false,
		pins: `["model_year","power_hp"]`,
		fields: []builtinField{
			{"make", "Make", "text"},
			{"model", "Model", "text"},
			{"model_year", "Year", "number"},
			{"displacement_cc", "Engine size (cc)", "number"},
			{"power_hp", "Power (hp)", "number"},
		},
	},
	{
		key: "wishlist", name: "Wishlist", icon: ptr("i:tag"), allowsPrice: true,
		pins: `["price"]`,
	},
}

// BuiltinTypes makes sure the built-in types of the workspace exist with their
// locked fields and default pins. It is idempotent: an existing row is never
// changed, so a user's edit of a locked label or of the pins survives. Pass the
// Queries of a transaction (or of the pool) from the caller.
func BuiltinTypes(ctx context.Context, q *sqlc.Queries, workspaceID uuid.UUID) error {
	for _, b := range builtins {
		if err := q.InsertBuiltinTypeDefinition(ctx, sqlc.InsertBuiltinTypeDefinitionParams{
			WorkspaceID:      workspaceID,
			Key:              b.key,
			Name:             b.name,
			DefaultIcon:      b.icon,
			AllowsPrice:      b.allowsPrice,
			DefaultSortField: b.sortField,
		}); err != nil {
			return fmt.Errorf("insert built-in type %s: %w", b.key, err)
		}
		if err := q.SetBuiltinDefaultPinnedFields(ctx, sqlc.SetBuiltinDefaultPinnedFieldsParams{
			Pins:        []byte(b.pins),
			WorkspaceID: workspaceID,
			Key:         b.key,
		}); err != nil {
			return fmt.Errorf("set default pins of built-in type %s: %w", b.key, err)
		}
		for i, f := range b.fields {
			if err := q.InsertBuiltinTypeField(ctx, sqlc.InsertBuiltinTypeFieldParams{
				Key:         f.key,
				Label:       f.label,
				FieldType:   f.fieldType,
				Position:    int32(i),
				WorkspaceID: workspaceID,
				TypeKey:     b.key,
			}); err != nil {
				return fmt.Errorf("insert field %s of built-in type %s: %w", f.key, b.key, err)
			}
		}
	}
	return nil
}

func ptr(s string) *string { return &s }
