// Package definitions manages workspace type and field definitions.
package definitions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/tree"
)

type DB interface {
	database.TxBeginner
	sqlc.DBTX
}
type Service struct {
	db          DB
	workspaceID uuid.UUID
}

func New(db DB, workspaceID uuid.UUID) *Service { return &Service{db: db, workspaceID: workspaceID} }

type Field struct {
	Key, Label, Type  string
	Position, Version int
	Locked            bool
}
type Type struct {
	Key, Name           string
	AllowsPrice, Locked bool
	Version             int
	DefaultIcon         *string
	DefaultSortField    *string
	DefaultPinnedFields *[]string
	Fields              []Field
}
type CreateType struct {
	Name                string
	DefaultIcon         *string
	AllowsPrice         bool
	DefaultSortField    *string
	DefaultPinnedFields *[]string
}

// NullableString distinguishes an omitted property from a supplied null.
type NullableString struct {
	Set   bool
	Value *string
}

// NullableStrings distinguishes an omitted property from a supplied null.
type NullableStrings struct {
	Set   bool
	Value *[]string
}

type UpdateType struct {
	Name                *string
	DefaultIcon         NullableString
	AllowsPrice         *bool
	DefaultSortField    NullableString
	DefaultPinnedFields NullableStrings
}
type CreateField struct{ Label, Type string }

const maxFieldLabelLen = 100

func (s *Service) List(ctx context.Context) ([]Type, error) {
	q := sqlc.New(s.db)
	rows, err := q.ListTypeDefinitions(ctx, s.workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Type, 0, len(rows))
	for _, r := range rows {
		fs, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: r.ID})
		if err != nil {
			return nil, err
		}
		k := typeRow(r)
		for _, f := range fs {
			k.Fields = append(k.Fields, fieldRow(f))
		}
		out = append(out, k)
	}
	return out, nil
}
func (s *Service) Create(ctx context.Context, in CreateType) (Type, error) {
	name, err := cleanTypeName(in.Name)
	if err != nil {
		return Type{}, err
	}
	if in.DefaultIcon != nil && !tree.ValidIcon(*in.DefaultIcon) {
		return Type{}, apperr.Validation(apperr.FieldError{Field: "default_icon", Message: "must be one emoji or i: followed by a lowercase name of 1 to 40 letters, digits or hyphens"})
	}
	key := keyFor(name)
	if in.DefaultSortField != nil && (!validFieldKey(*in.DefaultSortField) || (*in.DefaultSortField == "price" && !in.AllowsPrice)) {
		return Type{}, apperr.Validation(apperr.FieldError{Field: "default_sort_field", Message: "must be a field key"})
	}
	var pinnedJSON []byte
	if in.DefaultPinnedFields != nil {
		if len(*in.DefaultPinnedFields) > 3 {
			return Type{}, apperr.Validation(apperr.FieldError{Field: "default_pinned_fields", Message: "must have at most 3 entries"})
		}
		seen := map[string]bool{}
		for i, pin := range *in.DefaultPinnedFields {
			if (!validFieldKey(pin) || (pin == "price" && !in.AllowsPrice)) || seen[pin] {
				return Type{}, apperr.Validation(apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be a unique field key"})
			}
			seen[pin] = true
		}
		var err error
		pinnedJSON, err = json.Marshal(*in.DefaultPinnedFields)
		if err != nil {
			return Type{}, fmt.Errorf("marshal default pinned fields: %w", err)
		}
	}
	var row sqlc.TypeDefinition
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		var err error
		row, err = q.InsertTypeDefinition(ctx, sqlc.InsertTypeDefinitionParams{WorkspaceID: s.workspaceID, Key: key, Name: name, DefaultIcon: in.DefaultIcon, AllowsPrice: in.AllowsPrice, DefaultSortField: in.DefaultSortField, DefaultPinnedFields: pinnedJSON, Now: time.Now().UTC()})
		return err
	})
	if err != nil {
		return Type{}, err
	}
	return typeRow(row), nil
}

func validFieldKey(key string) bool {
	if key == "store_links" || len(key) == 0 || len(key) > 32 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, r := range key[1:] {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		if !valid {
			return false
		}
	}
	return true
}

func cleanTypeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", apperr.Validation(apperr.FieldError{Field: "name", Message: "must be 1 to 100 characters"})
	}
	return name, nil
}

func defaultPins(raw []byte) (*[]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var pins []string
	if err := json.Unmarshal(raw, &pins); err != nil {
		return nil, fmt.Errorf("decode default pinned fields: %w", err)
	}
	return &pins, nil
}

func validateDefaults(allowsPrice bool, fields []sqlc.TypeField, sort *string, pins *[]string) error {
	allowed := make(map[string]struct{}, len(fields)+1)
	sortable := make(map[string]struct{}, len(fields)+1)
	for _, field := range fields {
		allowed[field.Key] = struct{}{}
		if field.FieldType != "stores" {
			sortable[field.Key] = struct{}{}
		}
	}
	if allowsPrice {
		allowed["price"] = struct{}{}
		sortable["price"] = struct{}{}
	}
	var errs []apperr.FieldError
	// A stores field is pinnable but never a sort key (F-016).
	if sort != nil && (!validFieldKey(*sort) || !allowedField(sortable, *sort)) {
		errs = append(errs, apperr.FieldError{Field: "default_sort_field", Message: "must be a field key of this type"})
	}
	if pins != nil {
		if len(*pins) > 3 {
			errs = append(errs, apperr.FieldError{Field: "default_pinned_fields", Message: "must have at most 3 entries"})
		} else {
			seen := make(map[string]struct{}, len(*pins))
			for i, pin := range *pins {
				if !validFieldKey(pin) || !allowedField(allowed, pin) {
					errs = append(errs, apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be a field key of this type"})
					continue
				}
				if _, exists := seen[pin]; exists {
					errs = append(errs, apperr.FieldError{Field: fmt.Sprintf("default_pinned_fields[%d]", i), Message: "must be unique"})
				}
				seen[pin] = struct{}{}
			}
		}
	}
	if len(errs) > 0 {
		return apperr.Validation(errs...)
	}
	return nil
}

func allowedField(fields map[string]struct{}, key string) bool {
	_, ok := fields[key]
	return ok
}

func equalStringPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func equalStringSlices(a, b *[]string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return slices.Equal(*a, *b)
}
func (s *Service) Update(ctx context.Context, key string, v int, in UpdateType) (Type, error) {
	var out sqlc.TypeDefinition
	return s.mutateType(ctx, key, v, func(q *sqlc.Queries, cur sqlc.TypeDefinition) error {
		if cur.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "type", Message: "is locked"})
		}
		nextName := cur.Name
		if in.Name != nil {
			name, err := cleanTypeName(*in.Name)
			if err != nil {
				return err
			}
			nextName = name
		}
		nextIcon := cur.DefaultIcon
		if in.DefaultIcon.Set {
			nextIcon = in.DefaultIcon.Value
			if nextIcon != nil && !tree.ValidIcon(*nextIcon) {
				return apperr.Validation(apperr.FieldError{Field: "default_icon", Message: "must be one emoji or i: followed by a lowercase name of 1 to 40 letters, digits or hyphens"})
			}
		}
		nextAllowsPrice := cur.AllowsPrice
		if in.AllowsPrice != nil {
			nextAllowsPrice = *in.AllowsPrice
		}
		nextSort := cur.DefaultSortField
		if in.DefaultSortField.Set {
			nextSort = in.DefaultSortField.Value
		}
		nextPins, err := defaultPins(cur.DefaultPinnedFields)
		if err != nil {
			return err
		}
		currentPins := nextPins
		if in.DefaultPinnedFields.Set {
			nextPins = in.DefaultPinnedFields.Value
		}
		fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: cur.ID})
		if err != nil {
			return err
		}
		if err := validateDefaults(nextAllowsPrice, fields, nextSort, nextPins); err != nil {
			return err
		}
		if nextName == cur.Name && equalStringPtr(nextIcon, cur.DefaultIcon) && nextAllowsPrice == cur.AllowsPrice && equalStringPtr(nextSort, cur.DefaultSortField) && equalStringSlices(nextPins, currentPins) {
			out = cur
			return nil
		}
		var pinsJSON []byte
		if nextPins != nil {
			pinsJSON, err = json.Marshal(*nextPins)
			if err != nil {
				return fmt.Errorf("marshal default pinned fields: %w", err)
			}
		}
		out, err = q.UpdateTypeDefinition(ctx, sqlc.UpdateTypeDefinitionParams{
			ID: cur.ID, WorkspaceID: s.workspaceID, Name: nextName, SetName: in.Name != nil,
			DefaultIcon: nextIcon, SetDefaultIcon: in.DefaultIcon.Set,
			AllowsPrice: nextAllowsPrice, SetAllowsPrice: in.AllowsPrice != nil,
			DefaultSortField: nextSort, SetDefaultSortField: in.DefaultSortField.Set,
			DefaultPinnedFields: pinsJSON, SetDefaultPinnedFields: in.DefaultPinnedFields.Set,
			Now: time.Now().UTC(),
		})
		return err
	}, &out)
}
func (s *Service) mutateType(ctx context.Context, key string, v int, fn func(*sqlc.Queries, sqlc.TypeDefinition) error, out *sqlc.TypeDefinition) (Type, error) {
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, e := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: key})
		if e != nil {
			return apperr.NotFound("type")
		}
		cur, e = q.LockTypeDefinition(ctx, sqlc.LockTypeDefinitionParams{WorkspaceID: s.workspaceID, ID: cur.ID})
		if e != nil {
			return e
		}
		if int(cur.Version) != v {
			return apperr.VersionConflict(int(cur.Version))
		}
		return fn(q, cur)
	})
	if err != nil {
		return Type{}, err
	}
	return typeRow(*out), nil
}
func (s *Service) Delete(ctx context.Context, key string, v int) error {
	return database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, e := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: key})
		if e != nil {
			return apperr.NotFound("type")
		}
		cur, e = q.LockTypeDefinition(ctx, sqlc.LockTypeDefinitionParams{WorkspaceID: s.workspaceID, ID: cur.ID})
		if e != nil {
			return e
		}
		if int(cur.Version) != v {
			return apperr.VersionConflict(int(cur.Version))
		}
		if cur.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "type", Message: "is locked"})
		}
		lists, e := q.ListActiveListsByType(ctx, sqlc.ListActiveListsByTypeParams{WorkspaceID: s.workspaceID, Type: key})
		if e != nil {
			return e
		}
		if len(lists) > 0 {
			return apperr.Validation(apperr.FieldError{Field: "type", Message: fmt.Sprintf("is used by %d list(s), including %s", len(lists), lists[0].Name)})
		}
		if e = q.DeleteTypeFieldsOfType(ctx, sqlc.DeleteTypeFieldsOfTypeParams{TypeID: cur.ID, WorkspaceID: s.workspaceID}); e != nil {
			return e
		}
		return q.DeleteTypeDefinition(ctx, sqlc.DeleteTypeDefinitionParams{ID: cur.ID, WorkspaceID: s.workspaceID})
	})
}

// CreateField appends a custom field to a type. The type version serializes
// field-set edits so concurrent creates cannot generate the same key.
func (s *Service) CreateField(ctx context.Context, typeKey string, v int, in CreateField) (Field, error) {
	label, err := cleanFieldLabel(in.Label)
	if err != nil {
		return Field{}, err
	}
	if !customFieldType(in.Type) {
		return Field{}, apperr.Validation(apperr.FieldError{Field: "type", Message: "must be one of text, number, date, boolean, link"})
	}
	key := keyFor(label)
	if key == "" {
		return Field{}, apperr.Validation(apperr.FieldError{Field: "label", Message: "must contain a letter"})
	}
	if key[0] >= '0' && key[0] <= '9' {
		key = "field_" + key
	}
	if len(key) > 32 {
		key = strings.TrimRight(key[:32], "_")
	}

	var out sqlc.TypeField
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		typ, e := s.lockType(ctx, q, typeKey, v)
		if e != nil {
			return e
		}
		if _, e = q.GetTypeFieldByKey(ctx, sqlc.GetTypeFieldByKeyParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Key: key}); e == nil {
			return apperr.Validation(apperr.FieldError{Field: "label", Message: "generates an existing field key"})
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		position, e := q.NextTypeFieldPosition(ctx, sqlc.NextTypeFieldPositionParams{WorkspaceID: s.workspaceID, TypeID: typ.ID})
		if e != nil {
			return e
		}
		out, e = q.InsertTypeField(ctx, sqlc.InsertTypeFieldParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Key: key, Label: label, FieldType: in.Type, Position: position, Now: time.Now().UTC()})
		if e != nil {
			return e
		}
		return q.TouchTypeDefinition(ctx, sqlc.TouchTypeDefinitionParams{ID: typ.ID, WorkspaceID: s.workspaceID, Now: time.Now().UTC()})
	})
	if err != nil {
		return Field{}, err
	}
	return fieldRow(out), nil
}

func (s *Service) RenameField(ctx context.Context, typeKey, fieldKey string, v int, label string) (Field, error) {
	label, err := cleanFieldLabel(label)
	if err != nil {
		return Field{}, err
	}
	var out sqlc.TypeField
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		typ, e := s.lockTypeUnversioned(ctx, q, typeKey)
		if e != nil {
			return e
		}
		field, e := s.lockField(ctx, q, typ, fieldKey, v)
		if e != nil {
			return e
		}
		if field.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
		}
		out, e = q.UpdateTypeFieldLabel(ctx, sqlc.UpdateTypeFieldLabelParams{ID: field.ID, WorkspaceID: s.workspaceID, Label: label, Now: time.Now().UTC()})
		return e
	})
	if err != nil {
		return Field{}, err
	}
	return fieldRow(out), nil
}

// ReorderFields rewrites the order of custom fields only. Locked fields stay
// in their seed positions; keys must be exactly the current custom field set.
func (s *Service) ReorderFields(ctx context.Context, typeKey string, v int, keys []string) (int, error) {
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		typ, err := s.lockType(ctx, q, typeKey, v)
		if err != nil {
			return err
		}
		fields, err := q.ListTypeFields(ctx, sqlc.ListTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: typ.ID})
		if err != nil {
			return err
		}
		current := make([]string, 0, len(fields))
		for _, field := range fields {
			if !field.IsLocked {
				current = append(current, field.Key)
			}
		}
		if !sameStringSet(current, keys) {
			return apperr.OrderStale()
		}
		now := time.Now().UTC()
		if err := q.PrepareReorderTypeFields(ctx, sqlc.PrepareReorderTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Keys: keys, Now: now}); err != nil {
			return err
		}
		if err := q.ReorderTypeFields(ctx, sqlc.ReorderTypeFieldsParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Keys: keys, Now: now}); err != nil {
			return err
		}
		return q.TouchTypeDefinition(ctx, sqlc.TouchTypeDefinitionParams{ID: typ.ID, WorkspaceID: s.workspaceID, Now: now})
	})
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

// FieldDeletePreview reports the exact number of item values that DeleteField
// will remove, including trashed items.
func (s *Service) FieldDeletePreview(ctx context.Context, typeKey, fieldKey string) (int, error) {
	q := sqlc.New(s.db)
	typ, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: typeKey})
	if err != nil {
		return 0, apperr.NotFound("type")
	}
	field, err := q.GetTypeFieldByKey(ctx, sqlc.GetTypeFieldByKeyParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Key: fieldKey})
	if err != nil {
		return 0, apperr.NotFound("type field")
	}
	if field.IsLocked {
		return 0, apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
	}
	count, err := q.CountTypeFieldValues(ctx, sqlc.CountTypeFieldValuesParams{WorkspaceID: s.workspaceID, Type: typ.Key, Key: field.Key})
	return int(count), err
}

func (s *Service) DeleteField(ctx context.Context, typeKey, fieldKey string, v int) (int, error) {
	var count int32
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		typ, e := s.lockTypeUnversioned(ctx, q, typeKey)
		if e != nil {
			return e
		}
		field, e := s.lockField(ctx, q, typ, fieldKey, v)
		if e != nil {
			return e
		}
		if field.IsLocked {
			return apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
		}
		count, e = q.CountTypeFieldValues(ctx, sqlc.CountTypeFieldValuesParams{WorkspaceID: s.workspaceID, Type: typ.Key, Key: field.Key})
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		key := field.Key
		if e = q.ResetTypeFieldDefaults(ctx, sqlc.ResetTypeFieldDefaultsParams{ID: typ.ID, WorkspaceID: s.workspaceID, Key: &key, Now: now}); e != nil {
			return e
		}
		if e = q.ResetListTypeFieldPins(ctx, sqlc.ResetListTypeFieldPinsParams{WorkspaceID: s.workspaceID, Type: typ.Key, Key: field.Key, Now: now}); e != nil {
			return e
		}
		if e = q.ResetListTypeFieldSort(ctx, sqlc.ResetListTypeFieldSortParams{WorkspaceID: s.workspaceID, Type: typ.Key, Key: field.Key, Now: now}); e != nil {
			return e
		}
		if e = q.StripTypeFieldValues(ctx, sqlc.StripTypeFieldValuesParams{WorkspaceID: s.workspaceID, Type: typ.Key, Key: field.Key, Now: now}); e != nil {
			return e
		}
		return q.DeleteTypeField(ctx, sqlc.DeleteTypeFieldParams{ID: field.ID, WorkspaceID: s.workspaceID})
	})
	return int(count), err
}

func (s *Service) lockType(ctx context.Context, q *sqlc.Queries, key string, version int) (sqlc.TypeDefinition, error) {
	typ, err := s.lockTypeUnversioned(ctx, q, key)
	if err != nil {
		return sqlc.TypeDefinition{}, err
	}
	if int(typ.Version) != version {
		return sqlc.TypeDefinition{}, apperr.VersionConflict(int(typ.Version))
	}
	return typ, nil
}

func (s *Service) lockTypeUnversioned(ctx context.Context, q *sqlc.Queries, key string) (sqlc.TypeDefinition, error) {
	typ, err := q.GetTypeDefinitionByKey(ctx, sqlc.GetTypeDefinitionByKeyParams{WorkspaceID: s.workspaceID, Key: key})
	if err != nil {
		return sqlc.TypeDefinition{}, apperr.NotFound("type")
	}
	typ, err = q.LockTypeDefinition(ctx, sqlc.LockTypeDefinitionParams{ID: typ.ID, WorkspaceID: s.workspaceID})
	return typ, err
}

func (s *Service) lockField(ctx context.Context, q *sqlc.Queries, typ sqlc.TypeDefinition, key string, version int) (sqlc.TypeField, error) {
	field, err := q.GetTypeFieldByKey(ctx, sqlc.GetTypeFieldByKeyParams{WorkspaceID: s.workspaceID, TypeID: typ.ID, Key: key})
	if err != nil {
		return sqlc.TypeField{}, apperr.NotFound("type field")
	}
	field, err = q.LockTypeField(ctx, sqlc.LockTypeFieldParams{ID: field.ID, WorkspaceID: s.workspaceID})
	if err != nil {
		return sqlc.TypeField{}, err
	}
	if int(field.Version) != version {
		return sqlc.TypeField{}, apperr.VersionConflict(int(field.Version))
	}
	return field, nil
}

func cleanFieldLabel(label string) (string, error) {
	label = strings.TrimSpace(label)
	if n := utf8.RuneCountInString(label); n == 0 || n > maxFieldLabelLen {
		return "", apperr.Validation(apperr.FieldError{Field: "label", Message: "must be 1 to 100 characters"})
	}
	return label, nil
}

func customFieldType(fieldType string) bool {
	return fieldType == "text" || fieldType == "number" || fieldType == "date" || fieldType == "boolean" || fieldType == "link"
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]struct{}, len(a))
	for _, key := range a {
		seen[key] = struct{}{}
	}
	for _, key := range b {
		if _, ok := seen[key]; !ok {
			return false
		}
		delete(seen, key)
	}
	return len(seen) == 0
}
func typeRow(r sqlc.TypeDefinition) Type {
	k := Type{Key: r.Key, Name: r.Name, AllowsPrice: r.AllowsPrice, Locked: r.IsLocked, Version: int(r.Version), DefaultIcon: r.DefaultIcon, DefaultSortField: r.DefaultSortField}
	if len(r.DefaultPinnedFields) > 0 && string(r.DefaultPinnedFields) != "null" {
		var pins []string
		if json.Unmarshal(r.DefaultPinnedFields, &pins) == nil {
			k.DefaultPinnedFields = &pins
		}
	}
	return k
}
func fieldRow(r sqlc.TypeField) Field {
	return Field{Key: r.Key, Label: r.Label, Type: r.FieldType, Position: int(r.Position), Locked: r.IsLocked, Version: int(r.Version)}
}
func keyFor(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
