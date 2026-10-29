package folders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
)

const maxNameLen = 200

// Store opens transactions and reads single folders. Declared here, where it
// is used; *Postgres implements it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
	Get(ctx context.Context, id uuid.UUID) (Folder, error)
}

// Repo is the set of queries one transaction may run. Every method is one
// plain query; the rules that combine them live in Service.
type Repo interface {
	LockTree(ctx context.Context) error
	Get(ctx context.Context, id uuid.UUID) (Folder, error)
	GetForUpdate(ctx context.Context, id uuid.UUID) (Folder, error)
	ListActive(ctx context.Context) ([]Folder, error)
	ChildIDs(ctx context.Context, parent *uuid.UUID) ([]uuid.UUID, error)
	NextSortOrder(ctx context.Context, parent *uuid.UUID) (int, error)
	Insert(ctx context.Context, parent *uuid.UUID, name string, sortOrder int, now time.Time) (Folder, error)
	Rename(ctx context.Context, id uuid.UUID, name string, now time.Time) (Folder, error)
	Move(ctx context.Context, id uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) (Folder, error)
	SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error
}

// Service holds the folder rules. Structural writes take the tree lock first
// so two concurrent moves cannot both pass the cycle check.
type Service struct {
	store Store
	clock clock.Clock
}

func NewService(store Store, clk clock.Clock) *Service { return &Service{store: store, clock: clk} }

// Create appends a folder after its siblings. A missing or trashed parent is DestinationUnavailable.
func (s *Service) Create(ctx context.Context, name string, parent *uuid.UUID) (Folder, error) {
	name, err := cleanName(name)
	if err != nil {
		return Folder{}, err
	}
	var out Folder
	err = s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if err := requireActive(ctx, r, parent); err != nil {
			return err
		}
		next, err := r.NextSortOrder(ctx, parent)
		if err != nil {
			return fmt.Errorf("next sort order: %w", err)
		}
		out, err = r.Insert(ctx, parent, name, next, s.clock.Now())
		return wrap("insert folder", err)
	})
	return out, err
}

// Get returns an active folder.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Folder, error) { return s.store.Get(ctx, id) }

// Rename changes the name. A nil name changes nothing and does not bump the version.
func (s *Service) Rename(ctx context.Context, id uuid.UUID, version int, name *string) (Folder, error) {
	var clean string
	if name != nil {
		var err error
		if clean, err = cleanName(*name); err != nil {
			return Folder{}, err
		}
	}
	var out Folder
	err := s.store.InTx(ctx, func(r Repo) error {
		cur, err := lock(ctx, r, id, version)
		if err != nil {
			return err
		}
		if name == nil {
			out = cur
			return nil
		}
		out, err = r.Rename(ctx, id, clean, s.clock.Now())
		return wrap("rename folder", err)
	})
	return out, err
}

// Move re-parents a folder (nil = root), appended last. Errors in order:
// NotFound, VersionConflict, DestinationUnavailable, Cycle.
func (s *Service) Move(ctx context.Context, id uuid.UUID, version int, parent *uuid.UUID) (Folder, error) {
	var out Folder
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if _, err := lock(ctx, r, id, version); err != nil {
			return err
		}
		if err := requireActive(ctx, r, parent); err != nil {
			return err
		}
		if parent != nil {
			all, err := r.ListActive(ctx)
			if err != nil {
				return fmt.Errorf("list folders: %w", err)
			}
			if isSelfOrAncestor(all, id, *parent) {
				return apperr.Cycle()
			}
		}
		next, err := r.NextSortOrder(ctx, parent)
		if err != nil {
			return fmt.Errorf("next sort order: %w", err)
		}
		out, err = r.Move(ctx, id, parent, next, s.clock.Now())
		return wrap("move folder", err)
	})
	return out, err
}

// Reorder rewrites the order of the children of parent. ids must be exactly the
// active children, else OrderStale. A missing or trashed parent is NotFound.
func (s *Service) Reorder(ctx context.Context, parent *uuid.UUID, ids []uuid.UUID) error {
	return s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return fmt.Errorf("lock tree: %w", err)
		}
		if parent != nil {
			if _, err := r.Get(ctx, *parent); err != nil {
				return err
			}
		}
		current, err := r.ChildIDs(ctx, parent)
		if err != nil {
			return fmt.Errorf("list children: %w", err)
		}
		if !sameSet(current, ids) {
			return apperr.OrderStale()
		}
		now := s.clock.Now()
		for i, id := range ids {
			if err := r.SetSortOrder(ctx, id, i, now); err != nil {
				return fmt.Errorf("set sort order: %w", err)
			}
		}
		return nil
	})
}

// lock locks the active folder and checks the caller's version.
func lock(ctx context.Context, r Repo, id uuid.UUID, version int) (Folder, error) {
	cur, err := r.GetForUpdate(ctx, id)
	if err != nil {
		return Folder{}, err
	}
	if cur.Version != version {
		return Folder{}, apperr.VersionConflict(cur.Version)
	}
	return cur, nil
}

// requireActive reports DestinationUnavailable when id is set but not an active folder.
func requireActive(ctx context.Context, r Repo, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	if _, err := r.Get(ctx, *id); err != nil {
		var nf *apperr.NotFoundError
		if errors.As(err, &nf) {
			return apperr.DestinationUnavailable()
		}
		return fmt.Errorf("get destination: %w", err)
	}
	return nil
}

// isSelfOrAncestor walks up from destination and reports whether it meets folderID.
// The step cap keeps it finite even on corrupt data.
func isSelfOrAncestor(all []Folder, folderID, destination uuid.UUID) bool {
	parents := make(map[uuid.UUID]*uuid.UUID, len(all))
	for _, f := range all {
		parents[f.ID] = f.ParentID
	}
	cur := &destination
	for range len(all) + 1 {
		if cur == nil {
			return false
		}
		if *cur == folderID {
			return true
		}
		cur = parents[*cur]
	}
	return false
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxNameLen {
		return name, apperr.Validation(apperr.FieldError{Field: "name", Message: fmt.Sprintf("must be 1 to %d characters", maxNameLen)})
	}
	return name, nil
}

// sameSet reports whether got holds exactly the ids of want: no duplicates, nothing foreign or missing.
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
		delete(set, id)
	}
	return len(set) == 0
}

func wrap(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}
