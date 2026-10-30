package trash

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Purge deletes batch id for good: the root with its whole subtree (every batch
// inside it), the attachment rows of its items and, after the commit, their image
// files. A file that cannot be deleted is logged; the images sweeper removes it later.
func (t *Service) Purge(ctx context.Context, id uuid.UUID) error {
	if err := requireWorkspace(ctx); err != nil {
		return err
	}
	var keys []string
	err := t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		b, err := r.LockBatch(ctx, id)
		if err != nil {
			return err
		}
		keys, err = t.purgeBatch(ctx, r, b)
		return err
	})
	if err != nil {
		return err
	}
	t.deleteFiles(ctx, keys)
	return nil
}

// PurgeAll deletes every trash batch of the workspace for good, the same way
// Purge deletes one, all in one transaction under the tree lock. It returns the
// number of batches removed (nested batches that went with a purged root count
// too, as they do in the trash list). Files are deleted after the commit. An empty
// trash returns 0 and no error.
func (t *Service) PurgeAll(ctx context.Context) (int, error) {
	if err := requireWorkspace(ctx); err != nil {
		return 0, err
	}
	var (
		keys    []string
		removed int
	)
	err := t.store.InTx(ctx, func(r Repo) error {
		if err := r.LockTree(ctx); err != nil {
			return err
		}
		ids, err := r.BatchIDs(ctx)
		if err != nil {
			return err
		}
		keys, err = t.purgeBatches(ctx, r, ids)
		if err != nil {
			return err
		}
		removed = len(ids)
		return nil
	})
	if err != nil {
		return 0, err
	}
	t.deleteFiles(ctx, keys)
	return removed, nil
}

// purgeBatches purges each batch of ids in order, inside the caller's transaction.
// A batch already removed with the root of an earlier one is skipped. It returns
// the storage keys of every removed attachment.
func (t *Service) purgeBatches(ctx context.Context, r Repo, ids []uuid.UUID) ([]string, error) {
	var keys []string
	for _, id := range ids {
		b, err := r.LockBatch(ctx, id)
		if isNotFound(err) {
			// Removed with the root of an earlier batch in this loop.
			continue
		}
		if err != nil {
			return nil, err
		}
		batchKeys, err := t.purgeBatch(ctx, r, b)
		if err != nil {
			return nil, err
		}
		keys = append(keys, batchKeys...)
	}
	return keys, nil
}

// purgeBatch removes one locked batch inside the caller's transaction: the root
// with its whole subtree, the attachment rows of its items, and the batch rows
// that lost their root. Every row is deleted explicitly, children before parents,
// so nothing depends on ON DELETE CASCADE. It returns the storage keys of the
// removed attachments; the caller deletes those files after the commit.
func (t *Service) purgeBatch(ctx context.Context, r Repo, b Batch) ([]string, error) {
	sub, err := t.purgeSubtree(ctx, r, b)
	if err != nil {
		return nil, err
	}
	// Read the attachments before the items that point to them go.
	atts, err := r.ListAttachmentFiles(ctx, sub.items)
	if err != nil {
		return nil, err
	}
	if err := r.DeleteContentVersions(ctx, sub.items); err != nil {
		return nil, err
	}
	if err := r.DeleteLookupImports(ctx, sub.items); err != nil {
		return nil, err
	}
	if err := r.DeleteItems(ctx, sub.items); err != nil {
		return nil, err
	}
	if err := r.DeleteLists(ctx, sub.lists); err != nil {
		return nil, err
	}
	if err := r.DeleteFolders(ctx, sub.folders); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(atts))
	keys := make([]string, 0, 2*len(atts))
	for _, a := range atts {
		ids = append(ids, a.ID)
		keys = append(keys, a.StorageKey, a.ThumbnailStorageKey)
	}
	if err := r.DeleteAttachmentRows(ctx, ids); err != nil {
		return nil, err
	}
	// The batch itself first (its root may already be gone), then batches nested
	// in the subtree whose roots went with it.
	if err := r.DeleteBatch(ctx, b.ID); err != nil {
		return nil, err
	}
	if err := r.DeleteOrphanBatches(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}

// purgeSet is the ids of a purged root's subtree, in any state.
type purgeSet struct {
	folders, lists, items []uuid.UUID
}

// purgeSubtree collects the ids a purge of batch b removes. A folder root reaches
// its descendant folders, their lists and the items of those lists; a list root
// its items; an item root only itself.
func (t *Service) purgeSubtree(ctx context.Context, r Repo, b Batch) (purgeSet, error) {
	var s purgeSet
	switch b.RootType {
	case TypeFolder:
		nodes, err := r.FolderParents(ctx)
		if err != nil {
			return purgeSet{}, err
		}
		s.folders = subtree(nodes, b.RootID)
		s.lists, err = r.ListListIDsByFolders(ctx, s.folders)
		if err != nil {
			return purgeSet{}, err
		}
	case TypeList:
		s.lists = []uuid.UUID{b.RootID}
	case TypeItem:
		s.items = []uuid.UUID{b.RootID}
		return s, nil
	default:
		return purgeSet{}, fmt.Errorf("unknown trash root type %q", b.RootType)
	}
	items, err := r.ListItemIDsByLists(ctx, s.lists)
	if err != nil {
		return purgeSet{}, err
	}
	s.items = items
	return s, nil
}

// deleteFiles removes purged image files after the commit. A failure is logged
// only: the images sweeper removes the orphan later.
func (t *Service) deleteFiles(ctx context.Context, keys []string) {
	for _, k := range keys {
		if err := t.files.Delete(k); err != nil {
			t.log.WarnContext(context.WithoutCancel(ctx), "image file cleanup after purge failed; the sweeper will retry", "err", err)
		}
	}
}
