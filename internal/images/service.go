package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// Store opens transactions and runs the single-statement reads and deletes on
// the pool. Declared here, where it is used; *Postgres implements it and tests
// use a fake. Methods read the workspace from the context.
type Store interface {
	// InTx runs fn in one transaction: commit on nil, rollback on error.
	InTx(ctx context.Context, fn func(Repo) error) error
	GetAttachment(ctx context.Context, id uuid.UUID) (Attachment, error)
	DeleteAttachment(ctx context.Context, id uuid.UUID) (FileKeys, error)
	ListAttachmentKeys(ctx context.Context) ([]string, error)
	ListUnreferencedAttachments(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)
}

var _ Store = (*Postgres)(nil)

// ItemReader returns the item an image write answers with. *items.Service
// satisfies it.
type ItemReader interface {
	Get(ctx context.Context, id uuid.UUID) (items.Item, error)
}

// Service is the image service: the rules of setting, replacing and removing an
// item's image, serving stored files and sweeping orphans. The context of every
// call must carry the workspace (scope.With).
type Service struct {
	store Store
	files storage.FileStore
	items ItemReader
	clock clock.Clock
	log   *slog.Logger
}

// NewService returns the image service. items renders the updated item that
// every write returns.
func NewService(store Store, files storage.FileStore, items ItemReader, clk clock.Clock, log *slog.Logger) *Service {
	return &Service{store: store, files: files, items: items, clock: clk, log: log}
}

// SetUpload attaches an already validated upload (see Process) to an item,
// replacing any existing image of either source. version is the caller's
// If-Match version. filename is for display only and may be nil.
//
// Order: both files are written first; then one transaction checks the item
// (404, 412), inserts the attachment, points the item at it and removes the
// previous attachment row; after the commit the previous files are deleted.
func (s *Service) SetUpload(ctx context.Context, itemID uuid.UUID, version int, p *Processed, filename *string) (items.Item, error) {
	id := uuid.New()
	key, thumbKey := attachmentKeys(id, p.Ext)
	if _, err := s.files.Put(key, bytes.NewReader(p.Original)); err != nil {
		return items.Item{}, fmt.Errorf("store original: %w", err)
	}
	if _, err := s.files.Put(thumbKey, bytes.NewReader(p.Thumbnail)); err != nil {
		s.deleteFiles(ctx, key)
		return items.Item{}, fmt.Errorf("store thumbnail: %w", err)
	}

	var old *FileKeys
	err := s.store.InTx(ctx, func(r Repo) error {
		cur, err := s.lock(ctx, r, itemID, version)
		if err != nil {
			return err
		}
		err = r.InsertAttachment(ctx, Attachment{
			ID: id, StorageKey: key, ThumbnailStorageKey: thumbKey,
			OriginalFilename: cleanUploadName(filename), MediaType: p.MediaType, SizeBytes: int64(len(p.Original)),
			Width: p.Width, Height: p.Height, SHA256: p.SHA256[:],
		}, s.clock.Now())
		if err != nil {
			return err
		}
		if err := r.SetItemImageAttachment(ctx, itemID, id, s.clock.Now()); err != nil {
			return err
		}
		old, err = s.dropPrevious(ctx, r, cur)
		return err
	})
	if err != nil {
		s.discardUnreferenced(ctx, id, key, thumbKey)
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(ctx, itemID)
}

// SetURL points an item at an external image, replacing any existing image.
// Setting the URL it already has changes nothing and does not bump the version.
func (s *Service) SetURL(ctx context.Context, itemID uuid.UUID, version int, rawURL string) (items.Item, error) {
	clean, err := cleanImageURL(rawURL)
	if err != nil {
		return items.Item{}, err
	}
	var old *FileKeys
	err = s.store.InTx(ctx, func(r Repo) error {
		cur, err := s.lock(ctx, r, itemID, version)
		if err != nil {
			return err
		}
		if cur.URL != nil && *cur.URL == clean {
			return nil
		}
		if err := r.SetItemImageURL(ctx, itemID, clean, s.clock.Now()); err != nil {
			return err
		}
		old, err = s.dropPrevious(ctx, r, cur)
		return err
	})
	if err != nil {
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(ctx, itemID)
}

// Delete removes the image of an item. An item without an image is returned
// unchanged (no version bump), so repeating a DELETE is harmless.
func (s *Service) Delete(ctx context.Context, itemID uuid.UUID, version int) (items.Item, error) {
	var old *FileKeys
	err := s.store.InTx(ctx, func(r Repo) error {
		cur, err := s.lock(ctx, r, itemID, version)
		if err != nil {
			return err
		}
		if cur.URL == nil && cur.AttachmentID == nil {
			return nil
		}
		if err := r.ClearItemImage(ctx, itemID, s.clock.Now()); err != nil {
			return err
		}
		old, err = s.dropPrevious(ctx, r, cur)
		return err
	})
	if err != nil {
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(ctx, itemID)
}

// lock locks the active item and checks the If-Match version. NotFound and
// VersionConflict come from the repository and the check below.
func (s *Service) lock(ctx context.Context, r Repo, itemID uuid.UUID, version int) (ItemImageState, error) {
	cur, err := r.LockItem(ctx, itemID)
	if err != nil {
		return ItemImageState{}, err
	}
	if cur.Version != version {
		return ItemImageState{}, apperr.VersionConflict(cur.Version)
	}
	return cur, nil
}

// dropPrevious deletes the attachment row the item pointed to before this
// write (the item no longer references it, so the delete succeeds) and returns
// its file keys for deleteOld. It returns nil when there was no attachment.
func (s *Service) dropPrevious(ctx context.Context, r Repo, cur ItemImageState) (*FileKeys, error) {
	if cur.AttachmentID == nil {
		return nil, nil
	}
	keys, err := r.DeleteAttachment(ctx, *cur.AttachmentID)
	if err != nil {
		return nil, fmt.Errorf("delete previous attachment: %w", err)
	}
	return &keys, nil
}

// discardUnreferenced removes the files of a failed upload. It first checks
// that the attachment row does not exist: if the commit succeeded but its
// answer was lost, the files are in use and must stay. When in doubt it leaves
// them to the sweeper.
func (s *Service) discardUnreferenced(ctx context.Context, id uuid.UUID, keys ...string) {
	ctx = context.WithoutCancel(ctx)
	_, err := s.store.GetAttachment(ctx, id)
	var nf *apperr.NotFoundError
	if errors.As(err, &nf) {
		s.deleteFiles(ctx, keys...)
	}
}

// deleteOld removes the files of a replaced attachment after the commit.
func (s *Service) deleteOld(ctx context.Context, old *FileKeys) {
	if old != nil {
		s.deleteFiles(ctx, old.StorageKey, old.ThumbnailStorageKey)
	}
}

// deleteFiles removes files best effort. A failure is logged (no paths) and
// left to the sweeper: the files have no attachment row, so the next run
// deletes them once they are older than an hour.
func (s *Service) deleteFiles(ctx context.Context, keys ...string) {
	for _, k := range keys {
		if err := s.files.Delete(k); err != nil {
			s.log.WarnContext(context.WithoutCancel(ctx), "image file cleanup failed; the sweeper will retry", "err", err)
		}
	}
}

// File is a stored image ready to stream.
type File struct {
	Body      io.ReadCloser
	MediaType string
	Size      int64
}

// Open returns the original file (or its JPEG thumbnail) of an attachment.
// Files of trashed items are still served (trash keeps files); an unknown id,
// or a row whose file is missing, is NotFound.
func (s *Service) Open(ctx context.Context, id uuid.UUID, thumbnail bool) (File, error) {
	a, err := s.store.GetAttachment(ctx, id)
	if err != nil {
		return File{}, err
	}
	key, mediaType := a.StorageKey, a.MediaType
	if thumbnail {
		key, mediaType = a.ThumbnailStorageKey, "image/jpeg"
	}
	body, size, err := s.files.Open(key)
	if errors.Is(err, storage.ErrNotFound) {
		s.log.ErrorContext(ctx, "attachment row has no file", "attachment_id", id)
		return File{}, apperr.NotFound("attachment")
	}
	if err != nil {
		return File{}, fmt.Errorf("open attachment file: %w", err)
	}
	return File{Body: body, MediaType: mediaType, Size: size}, nil
}
