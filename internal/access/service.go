package access

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Repo is what the rules read. Every method is one plain query; the workspace comes from the
// context.
type Repo interface {
	// Snapshot reads every folder and list of the workspace in any state.
	Snapshot(ctx context.Context) (Snapshot, error)
	// ItemList returns the list of an item in any state; ok is false when the item does not exist.
	ItemList(ctx context.Context, id uuid.UUID) (list uuid.UUID, ok bool, err error)
	// AttachmentItem returns the item that uses an attachment (image or lookup poster); ok is false for an orphan.
	AttachmentItem(ctx context.Context, id uuid.UUID) (item uuid.UUID, ok bool, err error)
	// TrashRoot returns the root of one batch.
	TrashRoot(ctx context.Context, id uuid.UUID) (TrashRoot, bool, error)
	// TrashRoots returns the root of every batch of the workspace.
	TrashRoots(ctx context.Context) ([]TrashRoot, error)
	// TopOrder returns the user's position of their top-level folders and lists.
	TopOrder(ctx context.Context, user uuid.UUID) (folders, lists map[uuid.UUID]int, err error)
}

// Service answers visibility questions for the caller in the context (scope.UserID). A context
// without a user sees nothing.
type Service struct{ repo Repo }

func NewService(repo Repo) *Service { return &Service{repo: repo} }

type cacheKey struct{}

// snapshotCache loads the snapshot at most once per request.
type snapshotCache struct {
	once sync.Once
	snap Snapshot
	err  error
}

// WithCache returns ctx carrying a per-request snapshot cache. The snapshot is read lazily, by the
// first question, and reused by the later ones of the same request: the guard, the body checks and
// the filtered collections run before any write of that request. Do not use it across writes.
func WithCache(ctx context.Context) context.Context {
	if _, ok := ctx.Value(cacheKey{}).(*snapshotCache); ok {
		return ctx
	}
	return context.WithValue(ctx, cacheKey{}, &snapshotCache{})
}

func (s *Service) snapshot(ctx context.Context) (Snapshot, error) {
	if c, ok := ctx.Value(cacheKey{}).(*snapshotCache); ok {
		c.once.Do(func() { c.snap, c.err = s.repo.Snapshot(ctx) })
		return c.snap, c.err
	}
	return s.repo.Snapshot(ctx)
}

// Visible reports whether the caller may see the resource. A missing resource is not visible.
func (s *Service) Visible(ctx context.Context, kind Kind, id uuid.UUID) (bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return false, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return false, err
	}
	box, err := s.box(ctx, snap, kind, id)
	if err != nil {
		return false, err
	}
	return snap.Visible(user, box), nil
}

// box resolves the box of a resource of any kind.
func (s *Service) box(ctx context.Context, snap Snapshot, kind Kind, id uuid.UUID) (Box, error) {
	switch kind {
	case KindFolder:
		return snap.FolderBox(id), nil
	case KindList:
		return snap.ListBox(id), nil
	case KindItem:
		list, ok, err := s.repo.ItemList(ctx, id)
		if err != nil || !ok {
			return Box{}, err
		}
		return snap.ListBox(list), nil
	case KindAttachment:
		item, ok, err := s.repo.AttachmentItem(ctx, id)
		if err != nil || !ok {
			return Box{}, err
		}
		return s.box(ctx, snap, KindItem, item)
	case KindTrashBatch:
		root, ok, err := s.repo.TrashRoot(ctx, id)
		if err != nil || !ok {
			return Box{}, err
		}
		return s.rootBox(snap, root), nil
	}
	return Box{}, nil
}

// rootBox is the box of the row a trash batch was made for, whatever its state.
func (s *Service) rootBox(snap Snapshot, r TrashRoot) Box {
	switch r.Type {
	case rootFolder:
		return snap.FolderBox(r.RootID)
	case rootList:
		return snap.ListBox(r.RootID)
	case rootItem:
		if r.ItemList != nil {
			return snap.ListBox(*r.ItemList)
		}
	}
	return Box{}
}

// FolderVisible reports whether the caller may see the folder (any state).
func (s *Service) FolderVisible(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.Visible(ctx, KindFolder, id)
}

// ListVisible reports whether the caller may see the list (any state).
func (s *Service) ListVisible(ctx context.Context, id uuid.UUID) (bool, error) {
	return s.Visible(ctx, KindList, id)
}

// VisibleFolderIDs returns the active folders the caller may see.
func (s *Service) VisibleFolderIDs(ctx context.Context) (map[uuid.UUID]bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return map[uuid.UUID]bool{}, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.VisibleFolders(user), nil
}

// VisibleListIDs returns the active lists the caller may see.
func (s *Service) VisibleListIDs(ctx context.Context) (map[uuid.UUID]bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return map[uuid.UUID]bool{}, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.VisibleLists(user), nil
}

// VisibleBatchIDs returns the trash batches whose root's box the caller may see. Never nil.
func (s *Service) VisibleBatchIDs(ctx context.Context) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	user, ok := scope.UserID(ctx)
	if !ok {
		return out, nil
	}
	snap, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	roots, err := s.repo.TrashRoots(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range roots {
		if snap.Visible(user, s.rootBox(snap, r)) {
			out = append(out, r.BatchID)
		}
	}
	return out, nil
}

// TopOrder returns the caller's own position of their top-level folders and lists. An entry the
// caller never ordered is absent.
func (s *Service) TopOrder(ctx context.Context) (folders, lists map[uuid.UUID]int, err error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return map[uuid.UUID]int{}, map[uuid.UUID]int{}, nil
	}
	return s.repo.TopOrder(ctx, user)
}
