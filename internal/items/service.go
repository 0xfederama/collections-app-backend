package items

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items/types"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

// Store opens transactions and reads items and lists outside one. Declared here,
// where it is used: *Postgres implements it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
	// Get returns an active item with its list type; the service fills the rest.
	Get(ctx context.Context, id uuid.UUID) (Item, error)
	ListByList(ctx context.Context, listID uuid.UUID) ([]Item, error)
	ListInfo(ctx context.Context, listID uuid.UUID) (ListInfo, error)
	PathOf(ctx context.Context, listID uuid.UUID) ([]PathSegment, error)
	MaxContentVersions(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int, error)
}

var _ Store = (*Postgres)(nil)

// TypeDef is what the item rules read from a type definition.
type TypeDef struct {
	Key         string
	AllowsPrice bool
}

// TypeSource reads the definition and fields of a list type. A nil type (a list
// with no type) gives no definition and no fields, without error. An unknown key
// is apperr.NotFound("type"). The composition root builds the adapter over the
// type definitions, so this package does not import them.
type TypeSource interface {
	TypeFields(ctx context.Context, typ *string) (TypeDef, []types.Field, error)
}

// SettingsSource reads the workspace's default store and owned streaming
// services. A workspace without a settings row answers apperr.NotFound("settings").
type SettingsSource interface {
	PreferredStore(ctx context.Context) (string, error)
	OwnedStreamingServices(ctx context.Context) ([]string, error)
}

// Service holds the item rules. Structural writes (create, move, reorder) take the
// tree lock first (D-060). Cross-feature reads (the list type, the preferred
// store) happen before the transaction opens, never inside it.
type Service struct {
	store    Store
	types    TypeSource
	settings SettingsSource
	clock    clock.Clock
}

// NewService returns the item rules over the given store and sources.
func NewService(store Store, typeSource TypeSource, settings SettingsSource, clk clock.Clock) *Service {
	return &Service{store: store, types: typeSource, settings: settings, clock: clk}
}

// Create appends an item to the end of an active list. Errors in order: the field
// rules (422), NotFound list, the price rule of the list's type (422), then the
// metadata rules of the type (400 unknown field, 422). A stores field the request
// left unset takes the preferred store. The list, its type and the preferred store
// are read before the transaction; a type change in between is a TypeMismatch.
func (s *Service) Create(ctx context.Context, listID uuid.UUID, in CreateInput) (Item, error) {
	in, err := CleanCreate(in)
	if err != nil {
		return Item{}, err
	}
	// Each read failure is kept and returned at the point where the old code made it.
	info, infoErr := s.store.ListInfo(ctx, listID)
	var def TypeDef
	var fields []types.Field
	var typeErr error
	if infoErr == nil {
		def, fields, typeErr = s.types.TypeFields(ctx, info.Type)
	}
	preferred, prefErr := s.settings.PreferredStore(ctx)

	var id uuid.UUID
	err = s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		locked, err := r.ListInfo(ctx, listID)
		if err != nil {
			return err
		}
		// The list became readable here but its type was read earlier: the type changed
		// in between, and the metadata would be checked against the wrong definition.
		if infoErr != nil || !sameListType(locked.Type, info.Type) {
			return apperr.TypeMismatch()
		}
		if in.Price != nil {
			if typeErr != nil {
				return typeErr
			}
			if err := PriceAllowed(def.AllowsPrice); err != nil {
				return err
			}
		}
		if typeErr != nil {
			return unknownTypeErr(typeErr)
		}
		meta, err := ValidateMetadata(info.Type, fields, in.Metadata, nil)
		if err != nil {
			return err
		}
		meta, err = seedPreferredStores(fields, in.Metadata, meta, preferred, prefErr)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("encode metadata: %w", err)
		}
		if err := checkTitleFree(ctx, r, listID, TitleEntry{Title: in.Title, Metadata: raw}, uuid.Nil); err != nil {
			return err
		}
		next, err := r.NextSortOrder(ctx, listID)
		if err != nil {
			return err
		}
		id, err = r.Insert(ctx, Item{
			ListID: listID, Title: in.Title, Price: in.Price, Website: in.Website,
			Metadata: raw, SortOrder: next,
		}, s.now())
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return s.load(ctx, id)
}

// Get returns an active item with its content version and its path (the folders
// above its list, root first, then the list).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Item, error) {
	it, err := s.load(ctx, id)
	if err != nil {
		return Item{}, err
	}
	it.Path, err = s.store.PathOf(ctx, it.ListID)
	if err != nil {
		return Item{}, err
	}
	return it, nil
}

// List returns one page of the active items of an active list. Errors: NotFound
// list; ErrInvalidSort when the list's type has no definition or the sort is
// not available on it; ErrInvalidFilter; the rest is Query's. The list
// and its type are read before the page is computed in memory.
func (s *Service) List(ctx context.Context, listID uuid.UUID, p ListParams) (ItemPage, error) {
	info, err := s.store.ListInfo(ctx, listID)
	if err != nil {
		return ItemPage{}, err
	}
	p = p.Normalized(info.SortMode)
	def, fields, typeErr := s.types.TypeFields(ctx, info.Type)
	if isNotFound(typeErr) {
		return ItemPage{}, ErrInvalidSort
	}
	if typeErr != nil {
		return ItemPage{}, typeErr
	}
	all, err := s.store.ListByList(ctx, listID)
	if err != nil {
		return ItemPage{}, err
	}
	defs := ListDefs{SortMode: info.SortMode, AllowsPrice: def.AllowsPrice, Fields: fields}
	if p.Availability != "" && streamingType(info.Type) {
		// No settings row reads as no owned services: every platform counts.
		owned, err := s.settings.OwnedStreamingServices(ctx)
		if err != nil && !isNotFound(err) {
			return ItemPage{}, err
		}
		defs.Owned = owned
	}
	rows, total, err := Query(all, info.Type, defs, p)
	if err != nil {
		return ItemPage{}, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, it := range rows {
		ids = append(ids, it.ID)
	}
	versions, err := s.store.MaxContentVersions(ctx, ids)
	if err != nil {
		return ItemPage{}, err
	}
	for i := range rows {
		rows[i].ContentVersion = versions[rows[i].ID]
	}
	return ItemPage{Items: rows, Limit: p.Limit, Offset: p.Offset, Total: total}, nil
}

// Update applies a partial update. version is the caller's If-Match version.
// Errors in order: the field rules (422), NotFound item, VersionConflict, NotFound
// list, the price rule of the list's type (422), then the metadata rules (400, 422).
// A patch that changes nothing writes nothing and does not bump the version (D-059).
//
// The item's list type is read before the transaction. The transaction re-checks
// the item, its list and the type: a change in between is a VersionConflict, as
// lists.Update answers it.
func (s *Service) Update(ctx context.Context, id uuid.UUID, version int, p Patch) (Item, error) {
	p, err := CleanPatch(p)
	if err != nil {
		return Item{}, err
	}
	pre, preErr := s.store.Get(ctx, id)
	var def TypeDef
	var fields []types.Field
	var typeErr error
	if preErr == nil {
		def, fields, typeErr = s.types.TypeFields(ctx, pre.Type)
	}
	err = s.store.InTx(ctx, func(r Repo) error {
		// A patch with a title takes the tree lock before the row lock, the order Move
		// uses, so the duplicate check below cannot race a create or a move.
		if p.Title != nil {
			if err := r.LockTree(ctx); err != nil {
				return err
			}
		}
		cur, err := r.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		list, err := r.ListInfo(ctx, cur.ListID)
		if err != nil {
			return err
		}
		if preErr != nil || cur.ListID != pre.ListID || !sameListType(list.Type, pre.Type) {
			return apperr.VersionConflict(cur.Version)
		}
		if p.Price.Set && p.Price.Value != nil {
			if typeErr != nil {
				return typeErr
			}
			if err := PriceAllowed(def.AllowsPrice); err != nil {
				return err
			}
		}
		if p.Metadata != nil && typeErr != nil {
			return unknownTypeErr(typeErr)
		}
		next, changed, err := MergePatch(Editable{
			Title: cur.Title, Price: cur.Price, Website: cur.Website, Metadata: cur.Metadata,
		}, list.Type, fields, p)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		// Checked when the title or the provider identity changes (F-026).
		if next.Title != cur.Title || providerKey(next.Metadata) != providerKey(cur.Metadata) {
			if err := checkTitleFree(ctx, r, cur.ListID, TitleEntry{Title: next.Title, Metadata: next.Metadata}, id); err != nil {
				return err
			}
		}
		return r.Update(ctx, Item{
			ID: id, Title: next.Title, Price: next.Price, Website: next.Website, Metadata: next.Metadata,
		}, s.now())
	})
	if err != nil {
		return Item{}, err
	}
	return s.load(ctx, id)
}

// Move appends an item to the end of another active list. Errors in order:
// NotFound item, VersionConflict, DestinationUnavailable, NotFound list, then
// TypeMismatch when the destination has another type than the item's list (F-007).
func (s *Service) Move(ctx context.Context, id uuid.UUID, version int, dest uuid.UUID) (Item, error) {
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		cur, err := r.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if cur.Version != version {
			return apperr.VersionConflict(cur.Version)
		}
		destList, err := r.ListInfo(ctx, dest)
		if err != nil {
			if isNotFound(err) {
				return apperr.DestinationUnavailable()
			}
			return err
		}
		srcList, err := r.ListInfo(ctx, cur.ListID)
		if err != nil {
			return err
		}
		if !sameListType(destList.Type, srcList.Type) {
			return apperr.TypeMismatch()
		}
		if err := checkTitleFree(ctx, r, dest, TitleEntry{Title: cur.Title, Metadata: cur.Metadata}, id); err != nil {
			return err
		}
		next, err := r.NextSortOrder(ctx, dest)
		if err != nil {
			return err
		}
		return r.Move(ctx, id, dest, next, s.now())
	})
	if err != nil {
		return Item{}, err
	}
	return s.load(ctx, id)
}

// Reorder rewrites the order of an active list's items: the position in ids. ids
// must be exactly the active items of the list, else OrderStale. It works in any
// sort mode and does not bump item versions (DATA_MODEL §7).
func (s *Service) Reorder(ctx context.Context, listID uuid.UUID, ids []uuid.UUID) error {
	return s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		if _, err := r.ListInfo(ctx, listID); err != nil {
			return err
		}
		rows, err := r.ListByList(ctx, listID)
		if err != nil {
			return err
		}
		current := make([]uuid.UUID, 0, len(rows))
		for _, it := range rows {
			current = append(current, it.ID)
		}
		if !sameItemIDs(current, ids) {
			return apperr.OrderStale()
		}
		now := s.now()
		for i, id := range ids {
			if err := r.SetSortOrder(ctx, id, i, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// checkTitleFree answers DuplicateTitle when another active item of the list is
// the same entry (sameEntry: provider id when both have one, else title).
// Existing duplicates are left alone: only writes that set a title are checked.
// Callers hold the tree lock.
func checkTitleFree(ctx context.Context, r Repo, listID uuid.UUID, e TitleEntry, self uuid.UUID) error {
	rows, err := r.ListByList(ctx, listID)
	if err != nil {
		return err
	}
	if entryTaken(rows, e, self) {
		return apperr.DuplicateTitle()
	}
	return nil
}

// load reads an active item with its content version. It has no path.
func (s *Service) load(ctx context.Context, id uuid.UUID) (Item, error) {
	it, err := s.store.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	versions, err := s.store.MaxContentVersions(ctx, []uuid.UUID{id})
	if err != nil {
		return Item{}, err
	}
	it.ContentVersion = versions[id]
	return it, nil
}

func (s *Service) now() time.Time { return s.clock.Now() }

// seedPreferredStores fills every stores field the create request left unset
// (absent or null) with [preferred] (F-016, D-096). An explicit value, including
// [], wins. preferred and prefErr are the settings read before the transaction: a
// workspace without settings seeds nothing, and any other failure is returned only
// when a stores field needs the default.
func seedPreferredStores(fields []types.Field, requested, meta map[string]any, preferred string, prefErr error) (map[string]any, error) {
	var unset []string
	for _, f := range fields {
		if _, set := meta[f.Key]; f.Type == "stores" && !set && requested[f.Key] == nil {
			unset = append(unset, f.Key)
		}
	}
	if len(unset) == 0 {
		return meta, nil
	}
	if prefErr != nil {
		if isNotFound(prefErr) {
			return meta, nil
		}
		return nil, prefErr
	}
	out := maps.Clone(meta)
	for _, key := range unset {
		out[key] = []string{preferred}
	}
	return out, nil
}

// unknownTypeErr is the 422 of a metadata write whose list type has no definition.
func unknownTypeErr(err error) error {
	if isNotFound(err) {
		return apperr.Validation(apperr.FieldError{Field: "type", Message: "is not a known item type"})
	}
	return err
}

func isNotFound(err error) bool {
	var nf *apperr.NotFoundError
	return errors.As(err, &nf)
}

// sameListType compares two list types; nil (no type) equals only nil.
func sameListType(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// sameItemIDs reports whether got holds exactly the ids of want: same length, no
// duplicates, nothing foreign or missing.
func sameItemIDs(want, got []uuid.UUID) bool {
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
