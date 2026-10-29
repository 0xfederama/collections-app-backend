package lists

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

// Store opens transactions and reads single lists. Declared here, where it is
// used; *Postgres implements it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
	Get(ctx context.Context, id uuid.UUID) (List, error)
}

// Repo is the set of queries one transaction may run. Every method is one
// plain query (or a small read that combines rows in Go); the rules that
// combine them live in Service. Methods read the workspace from the context.
type Repo interface {
	LockTree(ctx context.Context) error
	// Get and GetForUpdate return an active list. Only Get and ListActive fill ItemCount.
	Get(ctx context.Context, id uuid.UUID) (List, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (List, error)
	ListActive(ctx context.Context) ([]List, error)
	ChildIDs(ctx context.Context, folder *uuid.UUID) ([]uuid.UUID, error)
	NextSortOrder(ctx context.Context, folder *uuid.UUID) (int, error)
	// Insert writes l (ID ignored) and returns the stored row.
	Insert(ctx context.Context, l List, now time.Time) (List, error)
	// Update writes the editable fields of the list l.ID (the service merges the patch first).
	Update(ctx context.Context, l List, now time.Time) (List, error)
	// UpdateType sets the type (nil = no type) and resets the pinned fields to the type default.
	UpdateType(ctx context.Context, id uuid.UUID, typ *string, now time.Time) (List, error)
	Move(ctx context.Context, id uuid.UUID, folder *uuid.UUID, sortOrder int, now time.Time) (List, error)
	SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error
	// FolderChain returns the ancestors of folderID and folderID itself, root first. A folder that is not active gives none.
	FolderChain(ctx context.Context, folderID uuid.UUID) ([]PathFolder, error)
	FolderExists(ctx context.Context, id uuid.UUID) (bool, error)
	// SEAM: moves to items in B3d
	ClearItemsMetadata(ctx context.Context, listID uuid.UUID, now time.Time) error
	// SEAM: moves to items in B3d
	ClearItemsPrice(ctx context.Context, listID uuid.UUID, now time.Time) error
}

// TypeSource reads the type of a list. A nil key is a list with no type (no info, no
// fields). An unknown key is apperr.NotFound("type"). The composition root builds it
// over the type definitions, so this package does not import them.
type TypeSource interface {
	TypeInfo(ctx context.Context, key *string) (TypeInfo, []FieldInfo, error)
}

// Service holds the list rules. Structural writes (create, move, reorder) and
// type changes take the tree lock first, so the order of the writes is the same
// as every other structural write.
type Service struct {
	store Store
	types TypeSource
	clock clock.Clock
}

func NewService(store Store, types TypeSource, clk clock.Clock) *Service {
	return &Service{store: store, types: types, clock: clk}
}

// Create appends a list after its active siblings. A missing or trashed folder is
// DestinationUnavailable. An empty display mode takes the default, an empty sort
// mode the type's default sort field (or manual).
func (s *Service) Create(ctx context.Context, in CreateInput) (List, error) {
	if in.DisplayMode == "" {
		in.DisplayMode = DefaultDisplayMode
	}
	if err := validateDescription(in.Description); err != nil {
		return List{}, err
	}
	in.Description = nilIfEmpty(in.Description)
	name, errs := validateListFields(&in.Name, &in.DisplayMode, &in.SortMode)
	errs = append(errs, typeErrors(in.Type)...)
	errs = append(errs, iconErrors(in.Icon, in.IconColor)...)
	if err := validate(errs); err != nil {
		return List{}, err
	}
	// The type is read before the transaction: the type source opens its own, and
	// nesting it would hold two pool connections. Its failure is kept and returned
	// inside the transaction, where it always came (after the tree lock).
	def, fields, typeErr := s.typeOf(ctx, in.Type)
	var id uuid.UUID
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if typeErr != nil {
			return typeErr
		}
		if in.SortMode == "" {
			in.SortMode = DefaultSortMode
			if def.DefaultSortField != nil {
				in.SortMode = *def.DefaultSortField
			}
		}
		if err := validateDefinitionSort(in.Type, in.SortMode, def.AllowsPrice, fields, "sort_mode"); err != nil {
			return err
		}
		if in.PinnedFields != nil {
			pins, err := validateDefinitionPins(def.AllowsPrice, fields, in.PinnedFields)
			if err != nil {
				return err
			}
			in.PinnedFields = pins
		}
		if err := requireFolder(ctx, r, in.FolderID); err != nil {
			return err
		}
		next, err := r.NextSortOrder(ctx, in.FolderID)
		if err != nil {
			return err
		}
		created, err := r.Insert(ctx, List{
			FolderID: in.FolderID, Name: name, Type: in.Type, Description: in.Description,
			DisplayMode: in.DisplayMode, SortMode: in.SortMode, PinnedFields: in.PinnedFields,
			Icon: in.Icon, IconColor: in.IconColor, SortOrder: next,
		}, s.clock.Now())
		if err != nil {
			return err
		}
		id = created.ID
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return s.store.Get(ctx, id)
}

// Get returns an active list with its path: the ancestor folders, root first
// (empty for a list at the root).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (List, error) {
	var out List
	err := s.store.InTx(ctx, func(r Repo) error {
		l, err := r.Get(ctx, id)
		if err != nil {
			return err
		}
		l.Path = []PathFolder{}
		if l.FolderID != nil {
			chain, err := r.FolderChain(ctx, *l.FolderID)
			if err != nil {
				return err
			}
			l.Path = chain
		}
		out = l
		return nil
	})
	return out, err
}

// Update applies a partial update. An empty patch changes nothing and does not
// bump the version. version is the caller's If-Match version.
//
// The type that applies is read before the transaction; if the locked row's type
// differs from the one read, the list changed under the caller: VersionConflict.
//
// A patch that sends a type takes the tree lock first, then locks the row. A type
// that differs from the stored one clears the metadata (and, when the new type has
// no price, the price) of all the list's items, trashed ones included, in the same
// transaction (F-007, F-009). The resulting sort and pins are checked against the
// type that applies after the patch (planUpdate).
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int, p Patch) (List, error) {
	p, err := normalizePatch(p)
	if err != nil {
		return List{}, err
	}
	// Read the row and the type before the transaction (see Create). The row read
	// only decides which type applies; the transaction re-reads the locked row and
	// answers a type that changed in between as a version conflict.
	pre, err := s.store.Get(ctx, id)
	if err != nil {
		return List{}, err
	}
	newType := pre.Type
	if p.Type.Set {
		newType = p.Type.Value
	}
	def, fields, typeErr := s.typeOf(ctx, newType)
	err = s.store.InTx(ctx, func(r Repo) error {
		if p.Type.Set {
			if err := r.LockTree(ctx); err != nil {
				return fmt.Errorf("lock tree: %w", err)
			}
		}
		cur, err := r.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		if !equalPtr(cur.Type, pre.Type) {
			return apperr.VersionConflict(cur.Version)
		}
		if typeErr != nil {
			return typeErr
		}
		plan, err := planUpdate(cur, def, fields, p)
		if err != nil {
			return err
		}
		return s.apply(ctx, r, id, plan)
	})
	if err != nil {
		return List{}, err
	}
	return s.store.Get(ctx, id)
}

// apply writes a plan inside the caller's transaction: the type change and what it
// clears first, then the row update. A plan that changes nothing writes nothing.
func (s *Service) apply(ctx context.Context, r Repo, id uuid.UUID, plan updatePlan) error {
	now := s.clock.Now()
	if plan.TypeChanged {
		if _, err := r.UpdateType(ctx, id, plan.NewType, now); err != nil {
			return err
		}
		if err := r.ClearItemsMetadata(ctx, id, now); err != nil {
			return err
		}
		if plan.ClearPrice {
			if err := r.ClearItemsPrice(ctx, id, now); err != nil {
				return err
			}
		}
	}
	if !plan.Write {
		return nil
	}
	_, err := r.Update(ctx, List{
		ID: id, Name: plan.Name, Description: plan.Description, DisplayMode: plan.DisplayMode, SortMode: plan.SortMode,
		PinnedFields: plan.PinnedFields, Icon: plan.Icon, IconColor: plan.IconColor,
	}, now)
	return err
}

// Move moves a list into a folder (nil = root), appended last. Errors in order:
// NotFound, VersionConflict, DestinationUnavailable.
func (s *Service) Move(ctx context.Context, id uuid.UUID, version int, folder *uuid.UUID) (List, error) {
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		cur, err := r.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		if err := requireFolder(ctx, r, folder); err != nil {
			return err
		}
		next, err := r.NextSortOrder(ctx, folder)
		if err != nil {
			return err
		}
		_, err = r.Move(ctx, id, folder, next, s.clock.Now())
		return err
	})
	if err != nil {
		return List{}, err
	}
	return s.store.Get(ctx, id)
}

// Reorder rewrites the order of the child lists of folder (nil = root). ids must
// be exactly the active children, else OrderStale. A missing or trashed folder is
// NotFound. The order is the position in ids; versions do not change.
func (s *Service) Reorder(ctx context.Context, folder *uuid.UUID, ids []uuid.UUID) error {
	return s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if folder != nil {
			ok, err := r.FolderExists(ctx, *folder)
			if err != nil {
				return err
			}
			if !ok {
				return apperr.NotFound("folder")
			}
		}
		current, err := r.ChildIDs(ctx, folder)
		if err != nil {
			return err
		}
		if !sameSet(current, ids) {
			return apperr.OrderStale()
		}
		now := s.clock.Now()
		for i, id := range ids {
			if err := r.SetSortOrder(ctx, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// typeOf reads the definition and fields of a list type. A nil type is a list with
// no type: no definition, no fields. Any failure to read the type is an unknown
// type (422), as it has always been.
func (s *Service) typeOf(ctx context.Context, typ *string) (TypeInfo, []FieldInfo, error) {
	if typ == nil {
		return TypeInfo{}, nil, nil
	}
	def, fields, err := s.types.TypeInfo(ctx, typ)
	if err != nil {
		return TypeInfo{}, nil, unknownListType()
	}
	return def, fields, nil
}

// requireFolder reports DestinationUnavailable when folder is set but is not an
// active folder. The root (nil) always exists.
func requireFolder(ctx context.Context, r Repo, folder *uuid.UUID) error {
	if folder == nil {
		return nil
	}
	ok, err := r.FolderExists(ctx, *folder)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.DestinationUnavailable()
	}
	return nil
}

// sameSet reports whether got holds exactly the ids of want: same length, no
// duplicates, nothing foreign or missing.
func sameSet(want, got []uuid.UUID) bool {
	if len(want) != len(got) {
		return false
	}
	set := make(map[uuid.UUID]bool, len(want))
	for _, id := range want {
		set[id] = true
	}
	for _, id := range got {
		if !set[id] {
			return false
		}
		delete(set, id) // a repeated id finds nothing the second time
	}
	return len(set) == 0
}
