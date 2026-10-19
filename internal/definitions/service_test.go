package definitions_test

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/definitions"
	"github.com/0xfederama/collections-app-backend/internal/testdb"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testdb.Terminate()
	os.Exit(code)
}

func newDefinitionsService(t *testing.T) (*definitions.Service, *definitionDB) {
	t.Helper()
	pool := testdb.NewMigrated(t, testdb.AdminURL(t))
	workspaceID := uuid.New()
	if _, err := pool.Exec(t.Context(), "INSERT INTO workspaces (id, name) VALUES ($1, 'W')", workspaceID); err != nil {
		t.Fatal(err)
	}
	return definitions.New(pool, workspaceID), &definitionDB{pool: pool, workspaceID: workspaceID}
}

type definitionDB struct {
	pool        *pgxpool.Pool
	workspaceID uuid.UUID
}

func TestF014B3_CustomFieldLifecycle(t *testing.T) {
	svc, _ := newDefinitionsService(t)
	kind, err := svc.Create(t.Context(), definitions.CreateKind{Name: "Board game"})
	if err != nil {
		t.Fatal(err)
	}
	rating, err := svc.CreateField(t.Context(), kind.Key, kind.Version, definitions.CreateField{Label: "Rating", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	age, err := svc.CreateField(t.Context(), kind.Key, kind.Version+1, definitions.CreateField{Label: "Player age", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	if age.Position != 1 || rating.Position != 0 {
		t.Fatalf("created positions = rating %d age %d", rating.Position, age.Position)
	}

	version, err := svc.ReorderFields(t.Context(), kind.Key, kind.Version+2, []string{age.Key, rating.Key})
	if err != nil || version != kind.Version+3 {
		t.Fatalf("ReorderFields() = %d, %v", version, err)
	}
	fields, err := svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, definition := range fields {
		if definition.Key == kind.Key {
			for _, field := range definition.Fields {
				got = append(got, field.Key)
			}
		}
	}
	if !slices.Equal(got, []string{age.Key, rating.Key}) {
		t.Errorf("field order = %v", got)
	}

	renamed, err := svc.RenameField(t.Context(), kind.Key, age.Key, age.Version+1, "Minimum player age")
	if err != nil || renamed.Label != "Minimum player age" || renamed.Version != age.Version+2 {
		t.Fatalf("RenameField() = %#v, %v", renamed, err)
	}
	if _, err := svc.RenameField(t.Context(), kind.Key, rating.Key, rating.Version, "Nope"); err == nil {
		t.Fatal("RenameField with stale ETag succeeded")
	} else {
		var conflict *apperr.VersionConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("RenameField stale error = %v", err)
		}
	}
}

func TestF014B4_CustomKindDefaultsAllowAtMostTwoPins(t *testing.T) {
	svc, _ := newDefinitionsService(t)
	pins := []string{"rating", "status"}
	sortField := "rating"
	kind, err := svc.Create(t.Context(), definitions.CreateKind{
		Name: "Board game", DefaultSortField: &sortField, DefaultPinnedFields: &pins,
	})
	if err != nil {
		t.Fatal(err)
	}
	if kind.DefaultSortField == nil || *kind.DefaultSortField != "rating" || kind.DefaultPinnedFields == nil || !slices.Equal(*kind.DefaultPinnedFields, pins) {
		t.Fatalf("kind defaults = %#v", kind)
	}
	tooMany := []string{"one", "two", "three"}
	if _, err := svc.Create(t.Context(), definitions.CreateKind{Name: "Too many", DefaultPinnedFields: &tooMany}); err == nil {
		t.Fatal("Create accepted more than two default pinned fields")
	}
}

func TestF014B3_UpdateCustomKindDefaultsAfterFieldsExist(t *testing.T) {
	svc, _ := newDefinitionsService(t)
	icon := "i:dices"
	kind, err := svc.Create(t.Context(), definitions.CreateKind{Name: "Board game", DefaultIcon: &icon})
	if err != nil {
		t.Fatal(err)
	}
	rating, err := svc.CreateField(t.Context(), kind.Key, kind.Version, definitions.CreateField{Label: "Rating", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	name := "Tabletop game"
	allowsPrice := true
	pins := []string{rating.Key, "price"}
	updated, err := svc.Update(t.Context(), kind.Key, kind.Version+1, definitions.UpdateKind{
		Name: &name, AllowsPrice: &allowsPrice,
		DefaultSortField:    definitions.NullableString{Set: true, Value: &rating.Key},
		DefaultPinnedFields: definitions.NullableStrings{Set: true, Value: &pins},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != name || updated.DefaultIcon == nil || *updated.DefaultIcon != icon || !updated.AllowsPrice || updated.DefaultSortField == nil || *updated.DefaultSortField != rating.Key || updated.DefaultPinnedFields == nil || !slices.Equal(*updated.DefaultPinnedFields, pins) {
		t.Fatalf("updated kind = %#v", updated)
	}

	cleared, err := svc.Update(t.Context(), kind.Key, updated.Version, definitions.UpdateKind{
		DefaultIcon:         definitions.NullableString{Set: true},
		DefaultPinnedFields: definitions.NullableStrings{Set: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.DefaultIcon != nil || cleared.DefaultPinnedFields != nil || cleared.DefaultSortField == nil || *cleared.DefaultSortField != rating.Key {
		t.Fatalf("cleared kind = %#v", cleared)
	}

	missing := "missing"
	if _, err := svc.Update(t.Context(), kind.Key, cleared.Version, definitions.UpdateKind{DefaultSortField: definitions.NullableString{Set: true, Value: &missing}}); err == nil {
		t.Fatal("Update accepted a default sort field that does not exist")
	} else {
		var validation *apperr.ValidationError
		if !errors.As(err, &validation) || len(validation.Errors) != 1 || validation.Errors[0].Field != "default_sort_field" {
			t.Fatalf("invalid default sort error = %#v", err)
		}
	}

	if _, err := svc.Update(t.Context(), "movie", 1, definitions.UpdateKind{Name: &name}); err == nil {
		t.Fatal("Update allowed a locked built-in kind")
	}
}

func TestF014B3_DeleteFieldPreviewsAndAtomicallyStripsValues(t *testing.T) {
	svc, db := newDefinitionsService(t)
	kind, err := svc.Create(t.Context(), definitions.CreateKind{Name: "Board game"})
	if err != nil {
		t.Fatal(err)
	}
	field, err := svc.CreateField(t.Context(), kind.Key, kind.Version, definitions.CreateField{Label: "Rating", Type: "number"})
	if err != nil {
		t.Fatal(err)
	}
	listID := uuid.New()
	if _, err := db.pool.Exec(t.Context(), `INSERT INTO lists (id, workspace_id, name, kind, sort_mode, pinned_fields, sort_order)
VALUES ($1, $2, 'Games', $3, 'rating', '["rating"]', 0)`, listID, db.workspaceID, kind.Key); err != nil {
		t.Fatal(err)
	}
	for i, metadata := range []string{`{"rating": 5, "notes": "keep"}`, `{"rating": 4}`} {
		if _, err := db.pool.Exec(t.Context(), `INSERT INTO items (id, workspace_id, list_id, title, metadata, sort_order)
VALUES ($1, $2, $3, $4, $5::jsonb, $6)`, uuid.New(), db.workspaceID, listID, "Game", metadata, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.pool.Exec(t.Context(), `UPDATE kind_definitions SET default_sort_field = $1, default_pinned_fields = '["rating"]' WHERE workspace_id = $2 AND id = (SELECT id FROM kind_definitions WHERE workspace_id = $2 AND key = $3)`, field.Key, db.workspaceID, kind.Key); err != nil {
		t.Fatal(err)
	}

	count, err := svc.FieldDeletePreview(t.Context(), kind.Key, field.Key)
	if err != nil || count != 2 {
		t.Fatalf("FieldDeletePreview() = %d, %v", count, err)
	}
	deleted, err := svc.DeleteField(t.Context(), kind.Key, field.Key, field.Version)
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteField() = %d, %v", deleted, err)
	}
	var values int
	if err := db.pool.QueryRow(t.Context(), `SELECT count(*) FROM items WHERE workspace_id = $1 AND metadata ? 'rating'`, db.workspaceID).Scan(&values); err != nil {
		t.Fatal(err)
	}
	if values != 0 {
		t.Errorf("rating values remaining = %d", values)
	}
	var listPins, listSort, defaultSort, defaultPins *string
	if err := db.pool.QueryRow(t.Context(), `SELECT l.pinned_fields::text, l.sort_mode, k.default_sort_field, k.default_pinned_fields::text
FROM lists l JOIN kind_definitions k ON k.workspace_id = l.workspace_id AND k.key = l.kind
WHERE l.id = $1`, listID).Scan(&listPins, &listSort, &defaultSort, &defaultPins); err != nil {
		t.Fatal(err)
	}
	if listPins != nil || listSort == nil || *listSort != "manual" || defaultSort != nil || defaultPins == nil || *defaultPins != "[]" {
		t.Errorf("field references remain: list pins=%v sort=%v default sort=%v pins=%v", listPins, listSort, defaultSort, defaultPins)
	}
}
