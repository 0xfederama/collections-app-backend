package definitions

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

// Store opens transactions. Declared here, where it is used; *Postgres implements
// it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

var _ Store = (*Postgres)(nil)

// Repo is the set of queries one transaction may run. Every method is one
// plain query; the rules that combine them live in Service. Methods read the
// workspace from the context. Lookups of a missing row are apperr.NotFound.
type Repo interface {
	ListTypes(ctx context.Context) ([]Type, error)
	GetTypeByKey(ctx context.Context, key string) (Type, error)
	GetTypeForUpdate(ctx context.Context, id uuid.UUID) (Type, error)
	TouchType(ctx context.Context, id uuid.UUID, now time.Time) error

	ListFields(ctx context.Context, typeID uuid.UUID) ([]Field, error)
	GetField(ctx context.Context, typeID uuid.UUID, key string) (Field, error)
	GetFieldForUpdate(ctx context.Context, id uuid.UUID) (Field, error)
	NextFieldPosition(ctx context.Context, typeID uuid.UUID) (int, error)
	InsertField(ctx context.Context, typeID uuid.UUID, f Field, now time.Time) (Field, error)
	UpdateFieldLabel(ctx context.Context, id uuid.UUID, label string, now time.Time) (Field, error)
	// ParkFieldPosition and SetFieldPosition are the two passes of a reorder. Park
	// writes no version; Set bumps the field version once.
	ParkFieldPosition(ctx context.Context, id uuid.UUID, position int, now time.Time) error
	SetFieldPosition(ctx context.Context, id uuid.UUID, position int, now time.Time) error
	DeleteField(ctx context.Context, id uuid.UUID) error

	// CountFieldValues counts the items of the type's lists (trashed ones too) that hold a value for the field.
	CountFieldValues(ctx context.Context, typeKey, fieldKey string) (int, error)
	StripFieldValues(ctx context.Context, typeKey, fieldKey string, now time.Time) error
	// ResetFieldDefaults drops the field from the type's default sort.
	ResetFieldDefaults(ctx context.Context, typeID uuid.UUID, fieldKey string, now time.Time) error
	ResetListFieldSort(ctx context.Context, typeKey, fieldKey string, now time.Time) error
}

// Service holds the type and field rules. Every write runs in one transaction
// that locks the type row (and the field row for field edits) before it checks
// the version the client sent, so a stale ETag writes nothing.
type Service struct {
	store Store
	clock clock.Clock
}

func NewService(store Store, clk clock.Clock) *Service { return &Service{store: store, clock: clk} }

// List returns every type of the workspace with its fields.
func (s *Service) List(ctx context.Context) ([]Type, error) {
	var out []Type
	err := s.store.InTx(ctx, func(r Repo) error {
		types, err := r.ListTypes(ctx)
		if err != nil {
			return err
		}
		for i := range types {
			types[i].Fields, err = r.ListFields(ctx, types[i].ID)
			if err != nil {
				return err
			}
		}
		out = types
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// CreateField appends a custom field to a type. The key comes from the label.
// The type version serializes field-set edits, so concurrent creates cannot
// generate the same key.
func (s *Service) CreateField(ctx context.Context, typeKey string, v int, in CreateField) (Field, error) {
	label, err := normalizeFieldLabel(in.Label)
	if err != nil {
		return Field{}, err
	}
	if !isCustomFieldType(in.Type) {
		return Field{}, apperr.Validation(apperr.FieldError{Field: "type", Message: "must be one of text, number, date, boolean, link"})
	}
	key, err := fieldKeyFromLabel(label)
	if err != nil {
		return Field{}, err
	}
	var out Field
	err = s.store.InTx(ctx, func(r Repo) error {
		typ, err := lockType(ctx, r, typeKey, v)
		if err != nil {
			return err
		}
		_, err = r.GetField(ctx, typ.ID, key)
		switch {
		case err == nil:
			return apperr.Validation(apperr.FieldError{Field: "label", Message: "generates an existing field key"})
		case !isNotFound(err):
			return err
		}
		position, err := r.NextFieldPosition(ctx, typ.ID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		out, err = r.InsertField(ctx, typ.ID, Field{Key: key, Label: label, Type: in.Type, Position: position}, now)
		if err != nil {
			return err
		}
		return r.TouchType(ctx, typ.ID, now)
	})
	if err != nil {
		return Field{}, err
	}
	return out, nil
}

// RenameField changes the label of a custom field. Locked fields keep theirs.
func (s *Service) RenameField(ctx context.Context, typeKey, fieldKey string, v int, label string) (Field, error) {
	label, err := normalizeFieldLabel(label)
	if err != nil {
		return Field{}, err
	}
	var out Field
	err = s.store.InTx(ctx, func(r Repo) error {
		typ, err := lockTypeUnversioned(ctx, r, typeKey)
		if err != nil {
			return err
		}
		field, err := lockField(ctx, r, typ, fieldKey, v)
		if err != nil {
			return err
		}
		if field.Locked {
			return apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
		}
		out, err = r.UpdateFieldLabel(ctx, field.ID, label, s.clock.Now())
		return err
	})
	if err != nil {
		return Field{}, err
	}
	return out, nil
}

// ReorderFields rewrites the order of custom fields only. Locked fields stay
// in their seed positions; keys must be exactly the current custom field set.
// It returns the new type version.
//
// The unique (type, position) constraint is not deferrable, so the order is
// written in two passes: pass 1 parks every custom field above the highest
// current position (no version bump), pass 2 sets the final positions, locked
// count plus index, with one version bump each.
func (s *Service) ReorderFields(ctx context.Context, typeKey string, v int, keys []string) (int, error) {
	err := s.store.InTx(ctx, func(r Repo) error {
		typ, err := lockType(ctx, r, typeKey, v)
		if err != nil {
			return err
		}
		fields, err := r.ListFields(ctx, typ.ID)
		if err != nil {
			return err
		}
		byKey := make(map[string]Field, len(fields))
		current := make([]string, 0, len(fields))
		locked := 0
		next := 0
		for _, field := range fields {
			byKey[field.Key] = field
			if field.Locked {
				locked++
			} else {
				current = append(current, field.Key)
			}
			if field.Position >= next {
				next = field.Position + 1
			}
		}
		if !sameKeySet(current, keys) {
			return apperr.OrderStale()
		}
		now := s.clock.Now()
		for i, key := range keys {
			if err := r.ParkFieldPosition(ctx, byKey[key].ID, next+i, now); err != nil {
				return err
			}
		}
		for i, key := range keys {
			if err := r.SetFieldPosition(ctx, byKey[key].ID, locked+i, now); err != nil {
				return err
			}
		}
		return r.TouchType(ctx, typ.ID, now)
	})
	if err != nil {
		return 0, err
	}
	return v + 1, nil
}

// FieldDeletePreview reports the exact number of item values that DeleteField
// will remove, including trashed items.
func (s *Service) FieldDeletePreview(ctx context.Context, typeKey, fieldKey string) (int, error) {
	var count int
	err := s.store.InTx(ctx, func(r Repo) error {
		typ, err := r.GetTypeByKey(ctx, typeKey)
		if err != nil {
			return err
		}
		field, err := r.GetField(ctx, typ.ID, fieldKey)
		if err != nil {
			return err
		}
		if field.Locked {
			return apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
		}
		count, err = r.CountFieldValues(ctx, typ.Key, field.Key)
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// DeleteField removes a custom field and, in the same transaction, the values
// stored in items, the list sorts on it, and the type defaults that name
// it. It returns the number of item values removed.
func (s *Service) DeleteField(ctx context.Context, typeKey, fieldKey string, v int) (int, error) {
	var count int
	err := s.store.InTx(ctx, func(r Repo) error {
		typ, err := lockTypeUnversioned(ctx, r, typeKey)
		if err != nil {
			return err
		}
		field, err := lockField(ctx, r, typ, fieldKey, v)
		if err != nil {
			return err
		}
		if field.Locked {
			return apperr.Validation(apperr.FieldError{Field: "field", Message: "is locked"})
		}
		count, err = r.CountFieldValues(ctx, typ.Key, field.Key)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if err := r.ResetFieldDefaults(ctx, typ.ID, field.Key, now); err != nil {
			return err
		}
		if err := r.ResetListFieldSort(ctx, typ.Key, field.Key, now); err != nil {
			return err
		}
		if err := r.StripFieldValues(ctx, typ.Key, field.Key, now); err != nil {
			return err
		}
		return r.DeleteField(ctx, field.ID)
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// lockTypeUnversioned finds a type by key and locks its row.
func lockTypeUnversioned(ctx context.Context, r Repo, key string) (Type, error) {
	typ, err := r.GetTypeByKey(ctx, key)
	if err != nil {
		return Type{}, err
	}
	return r.GetTypeForUpdate(ctx, typ.ID)
}

// lockType locks a type and checks the version the client sent.
func lockType(ctx context.Context, r Repo, key string, version int) (Type, error) {
	typ, err := lockTypeUnversioned(ctx, r, key)
	if err != nil {
		return Type{}, err
	}
	if typ.Version != version {
		return Type{}, apperr.VersionConflict(typ.Version)
	}
	return typ, nil
}

// lockField finds a field of typ by key, locks its row and checks its version.
func lockField(ctx context.Context, r Repo, typ Type, key string, version int) (Field, error) {
	field, err := r.GetField(ctx, typ.ID, key)
	if err != nil {
		return Field{}, err
	}
	field, err = r.GetFieldForUpdate(ctx, field.ID)
	if err != nil {
		return Field{}, err
	}
	if field.Version != version {
		return Field{}, apperr.VersionConflict(field.Version)
	}
	return field, nil
}

// isNotFound reports whether err is apperr.NotFound (any resource).
func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}
