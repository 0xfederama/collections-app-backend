package definitions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Postgres implements the repository on a pool (DB is declared in service.go).
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// repo runs the queries of one transaction. The workspace comes from the
// context on every call (scope), never from a field.
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) ListTypes(ctx context.Context) ([]Type, error) {
	rows, err := r.q.ListTypeDefinitions(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	out := make([]Type, 0, len(rows))
	for _, row := range rows {
		out = append(out, typeFromRow(row))
	}
	return out, nil
}

func (r *repo) GetTypeByKey(ctx context.Context, key string) (Type, error) {
	row, err := r.q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: scope.WorkspaceID(ctx), Key: key})
	if err != nil {
		return Type{}, notFound(err, "type")
	}
	return typeFromRow(row), nil
}

// GetTypeForUpdate locks the type row (sqlc's LockTypeDefinition).
func (r *repo) GetTypeForUpdate(ctx context.Context, id uuid.UUID) (Type, error) {
	row, err := r.q.LockTypeDefinition(ctx, sqlc.LockTypeDefinitionParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Type{}, notFound(err, "type")
	}
	return typeFromRow(row), nil
}

func (r *repo) InsertType(ctx context.Context, t Type, now time.Time) (Type, error) {
	pins, err := pinsToJSON(t.DefaultPinnedFields)
	if err != nil {
		return Type{}, err
	}
	row, err := r.q.InsertTypeDefinition(ctx, sqlc.InsertTypeDefinitionParams{
		WorkspaceID: scope.WorkspaceID(ctx), Key: t.Key, Name: t.Name, DefaultIcon: t.DefaultIcon,
		AllowsPrice: t.AllowsPrice, DefaultSortField: t.DefaultSortField, DefaultPinnedFields: pins, Now: now,
	})
	if err != nil {
		return Type{}, fmt.Errorf("insert type: %w", err)
	}
	return typeFromRow(row), nil
}

// UpdateType sets every editable column. The Set flags of the query are all
// true: the row is locked and t already holds the merged values.
func (r *repo) UpdateType(ctx context.Context, t Type, now time.Time) (Type, error) {
	pins, err := pinsToJSON(t.DefaultPinnedFields)
	if err != nil {
		return Type{}, err
	}
	row, err := r.q.UpdateTypeDefinition(ctx, sqlc.UpdateTypeDefinitionParams{
		ID: t.ID, WorkspaceID: scope.WorkspaceID(ctx),
		Name: t.Name, SetName: true,
		DefaultIcon: t.DefaultIcon, SetDefaultIcon: true,
		AllowsPrice: t.AllowsPrice, SetAllowsPrice: true,
		DefaultSortField: t.DefaultSortField, SetDefaultSortField: true,
		DefaultPinnedFields: pins, SetDefaultPinnedFields: true,
		Now: now,
	})
	if err != nil {
		return Type{}, notFound(err, "type")
	}
	return typeFromRow(row), nil
}

func (r *repo) DeleteType(ctx context.Context, id uuid.UUID) error {
	return r.q.DeleteTypeDefinition(ctx, sqlc.DeleteTypeDefinitionParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
}

func (r *repo) TouchType(ctx context.Context, id uuid.UUID, now time.Time) error {
	return r.q.TouchTypeDefinition(ctx, sqlc.TouchTypeDefinitionParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Now: now})
}

func (r *repo) ListActiveListsByType(ctx context.Context, typeKey string) ([]ListRef, error) {
	rows, err := r.q.ListActiveListsByType(ctx, sqlc.ListActiveListsByTypeParams{WorkspaceID: scope.WorkspaceID(ctx), Type: typeKey})
	if err != nil {
		return nil, fmt.Errorf("list lists of type: %w", err)
	}
	out := make([]ListRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, ListRef{ID: row.ID, Name: row.Name})
	}
	return out, nil
}

func (r *repo) ListStoresFields(ctx context.Context) ([]StoresField, error) {
	rows, err := r.q.ListStoresFieldKeys(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list stores fields: %w", err)
	}
	out := make([]StoresField, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoresField{TypeKey: row.TypeKey, FieldKey: row.FieldKey})
	}
	return out, nil
}

func (r *repo) ListFields(ctx context.Context, typeID uuid.UUID) ([]Field, error) {
	rows, err := r.q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: scope.WorkspaceID(ctx), TypeID: typeID})
	if err != nil {
		return nil, fmt.Errorf("list type fields: %w", err)
	}
	out := make([]Field, 0, len(rows))
	for _, row := range rows {
		out = append(out, fieldFromRow(row))
	}
	return out, nil
}

func (r *repo) GetField(ctx context.Context, typeID uuid.UUID, key string) (Field, error) {
	row, err := r.q.GetTypeFieldByKey(ctx, sqlc.GetTypeFieldByKeyParams{WorkspaceID: scope.WorkspaceID(ctx), TypeID: typeID, Key: key})
	if err != nil {
		return Field{}, notFound(err, "type field")
	}
	return fieldFromRow(row), nil
}

// GetFieldForUpdate locks the field row (sqlc's LockTypeField).
func (r *repo) GetFieldForUpdate(ctx context.Context, id uuid.UUID) (Field, error) {
	row, err := r.q.LockTypeField(ctx, sqlc.LockTypeFieldParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Field{}, notFound(err, "type field")
	}
	return fieldFromRow(row), nil
}

func (r *repo) NextFieldPosition(ctx context.Context, typeID uuid.UUID) (int, error) {
	next, err := r.q.NextTypeFieldPosition(ctx, sqlc.NextTypeFieldPositionParams{WorkspaceID: scope.WorkspaceID(ctx), TypeID: typeID})
	if err != nil {
		return 0, fmt.Errorf("next type field position: %w", err)
	}
	return int(next), nil
}

func (r *repo) InsertField(ctx context.Context, typeID uuid.UUID, f Field, now time.Time) (Field, error) {
	row, err := r.q.InsertTypeField(ctx, sqlc.InsertTypeFieldParams{
		WorkspaceID: scope.WorkspaceID(ctx), TypeID: typeID, Key: f.Key, Label: f.Label,
		FieldType: f.Type, Position: int32(f.Position), Now: now,
	})
	if err != nil {
		return Field{}, fmt.Errorf("insert type field: %w", err)
	}
	return fieldFromRow(row), nil
}

func (r *repo) UpdateFieldLabel(ctx context.Context, id uuid.UUID, label string, now time.Time) (Field, error) {
	row, err := r.q.UpdateTypeFieldLabel(ctx, sqlc.UpdateTypeFieldLabelParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Label: label, Now: now})
	if err != nil {
		return Field{}, notFound(err, "type field")
	}
	return fieldFromRow(row), nil
}

func (r *repo) ParkFieldPosition(ctx context.Context, id uuid.UUID, position int, now time.Time) error {
	return r.q.ParkTypeFieldPosition(ctx, sqlc.ParkTypeFieldPositionParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Position: int32(position), Now: now})
}

func (r *repo) SetFieldPosition(ctx context.Context, id uuid.UUID, position int, now time.Time) error {
	return r.q.SetTypeFieldPosition(ctx, sqlc.SetTypeFieldPositionParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Position: int32(position), Now: now})
}

func (r *repo) DeleteField(ctx context.Context, id uuid.UUID) error {
	return r.q.DeleteTypeField(ctx, sqlc.DeleteTypeFieldParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
}

func (r *repo) DeleteFieldsOfType(ctx context.Context, typeID uuid.UUID) error {
	return r.q.DeleteTypeFieldsOfType(ctx, sqlc.DeleteTypeFieldsOfTypeParams{TypeID: typeID, WorkspaceID: scope.WorkspaceID(ctx)})
}

func (r *repo) CountFieldValues(ctx context.Context, typeKey, fieldKey string) (int, error) {
	count, err := r.q.CountTypeFieldValues(ctx, sqlc.CountTypeFieldValuesParams{WorkspaceID: scope.WorkspaceID(ctx), Type: typeKey, Key: fieldKey})
	if err != nil {
		return 0, fmt.Errorf("count field values: %w", err)
	}
	return int(count), nil
}

func (r *repo) StripFieldValues(ctx context.Context, typeKey, fieldKey string, now time.Time) error {
	return r.q.StripTypeFieldValues(ctx, sqlc.StripTypeFieldValuesParams{WorkspaceID: scope.WorkspaceID(ctx), Type: typeKey, Key: fieldKey, Now: now})
}

func (r *repo) ResetFieldDefaults(ctx context.Context, typeID uuid.UUID, fieldKey string, now time.Time) error {
	return r.q.ResetTypeFieldDefaults(ctx, sqlc.ResetTypeFieldDefaultsParams{ID: typeID, WorkspaceID: scope.WorkspaceID(ctx), Key: &fieldKey, Now: now})
}

func (r *repo) ResetListFieldPins(ctx context.Context, typeKey, fieldKey string, now time.Time) error {
	return r.q.ResetListTypeFieldPins(ctx, sqlc.ResetListTypeFieldPinsParams{WorkspaceID: scope.WorkspaceID(ctx), Type: typeKey, Key: fieldKey, Now: now})
}

func (r *repo) ResetListFieldSort(ctx context.Context, typeKey, fieldKey string, now time.Time) error {
	return r.q.ResetListTypeFieldSort(ctx, sqlc.ResetListTypeFieldSortParams{WorkspaceID: scope.WorkspaceID(ctx), Type: typeKey, Key: fieldKey, Now: now})
}

func typeFromRow(r sqlc.TypeDefinition) Type {
	return Type{
		ID: r.ID, Key: r.Key, Name: r.Name, AllowsPrice: r.AllowsPrice, Locked: r.IsLocked, Version: int(r.Version),
		DefaultIcon: r.DefaultIcon, DefaultSortField: r.DefaultSortField, DefaultPinnedFields: pinsFromJSON(r.DefaultPinnedFields),
	}
}

func fieldFromRow(r sqlc.TypeField) Field {
	return Field{ID: r.ID, Key: r.Key, Label: r.Label, Type: r.FieldType, Position: int(r.Position), Locked: r.IsLocked, Version: int(r.Version)}
}

// pinsFromJSON reads the stored column. NULL, "null" and anything unreadable are nil.
func pinsFromJSON(raw []byte) *[]string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var pins []string
	if json.Unmarshal(raw, &pins) != nil {
		return nil
	}
	return &pins
}

// pinsToJSON writes the column: nil is NULL (no default pins), a nil slice is [].
func pinsToJSON(pins *[]string) ([]byte, error) {
	if pins == nil {
		return nil, nil
	}
	if *pins == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(*pins)
	if err != nil {
		return nil, fmt.Errorf("marshal default pinned fields: %w", err)
	}
	return b, nil
}

// notFound turns "no rows" into the domain error for resource; other errors pass through.
func notFound(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return err
}
