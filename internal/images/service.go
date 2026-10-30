// Package images owns item pictures: validating and storing uploads, setting
// an external image URL, removing images, serving stored files and sweeping
// orphans (DATA_MODEL §5, ARCHITECTURE §4 and §5). It knows nothing about HTTP.
//
// File and database consistency: files are written before the transaction
// that references them and deleted after the transaction that unreferences
// them commits. A crash leaves orphan files at worst, never a database row
// pointing at a missing file. The sweeper (sweeper.go) removes the leftovers.
package images

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

const (
	maxURLLen      = 2048
	maxFilenameLen = 255
)

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service is the image service of one workspace.
type Service struct {
	db          DB
	store       storage.FileStore
	clock       clock.Clock
	items       *items.Service
	log         *slog.Logger
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace. items renders the
// updated item that every write returns.
func New(db DB, store storage.FileStore, clk clock.Clock, itemSvc *items.Service, log *slog.Logger, workspaceID uuid.UUID) *Service {
	return &Service{db: db, store: store, clock: clk, items: itemSvc, log: log, workspaceID: workspaceID}
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
	key, thumbKey := storageKeys(id, p.Ext)
	if _, err := s.store.Put(key, bytes.NewReader(p.Original)); err != nil {
		return items.Item{}, fmt.Errorf("store original: %w", err)
	}
	if _, err := s.store.Put(thumbKey, bytes.NewReader(p.Thumbnail)); err != nil {
		s.deleteFiles(ctx, key)
		return items.Item{}, fmt.Errorf("store thumbnail: %w", err)
	}

	var old *sqlc.DeleteAttachmentRow
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, err := s.lock(ctx, q, itemID, version)
		if err != nil {
			return err
		}
		err = q.InsertAttachment(ctx, sqlc.InsertAttachmentParams{
			ID: id, WorkspaceID: s.workspaceID, StorageKey: key, ThumbnailStorageKey: thumbKey,
			OriginalFilename: cleanFilename(filename), MediaType: p.MediaType, SizeBytes: int64(len(p.Original)),
			Width: int32(p.Width), Height: int32(p.Height), Sha256: p.SHA256[:], Now: s.clock.Now(), //nolint:gosec // bounded by the 40 MP cap
		})
		if err != nil {
			return fmt.Errorf("insert attachment: %w", err)
		}
		if err := q.SetItemImageAttachment(ctx, sqlc.SetItemImageAttachmentParams{
			AttachmentID: uuid.NullUUID{UUID: id, Valid: true}, Now: s.clock.Now(), ID: itemID, WorkspaceID: s.workspaceID,
		}); err != nil {
			return fmt.Errorf("set item image: %w", err)
		}
		old, err = s.dropPrevious(ctx, q, cur)
		return err
	})
	if err != nil {
		s.discardUnreferenced(ctx, id, key, thumbKey)
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(scope.With(ctx, s.workspaceID), itemID)
}

// SetURL points an item at an external image, replacing any existing image.
// Setting the URL it already has changes nothing and does not bump the version.
func (s *Service) SetURL(ctx context.Context, itemID uuid.UUID, version int, rawURL string) (items.Item, error) {
	clean, err := cleanURL(rawURL)
	if err != nil {
		return items.Item{}, err
	}
	var old *sqlc.DeleteAttachmentRow
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, err := s.lock(ctx, q, itemID, version)
		if err != nil {
			return err
		}
		if cur.ImageUrl != nil && *cur.ImageUrl == clean {
			return nil
		}
		if err := q.SetItemImageURL(ctx, sqlc.SetItemImageURLParams{ImageUrl: &clean, Now: s.clock.Now(), ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("set item image url: %w", err)
		}
		old, err = s.dropPrevious(ctx, q, cur)
		return err
	})
	if err != nil {
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(scope.With(ctx, s.workspaceID), itemID)
}

// Delete removes the image of an item. An item without an image is returned
// unchanged (no version bump), so repeating a DELETE is harmless.
func (s *Service) Delete(ctx context.Context, itemID uuid.UUID, version int) (items.Item, error) {
	var old *sqlc.DeleteAttachmentRow
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		cur, err := s.lock(ctx, q, itemID, version)
		if err != nil {
			return err
		}
		if cur.ImageUrl == nil && !cur.ImageAttachmentID.Valid {
			return nil
		}
		if err := q.ClearItemImage(ctx, sqlc.ClearItemImageParams{Now: s.clock.Now(), ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
			return fmt.Errorf("clear item image: %w", err)
		}
		old, err = s.dropPrevious(ctx, q, cur)
		return err
	})
	if err != nil {
		return items.Item{}, err
	}
	s.deleteOld(ctx, old)
	return s.items.Get(scope.With(ctx, s.workspaceID), itemID)
}

// lock loads the active item under a row lock and checks the If-Match version.
func (s *Service) lock(ctx context.Context, q *sqlc.Queries, itemID uuid.UUID, version int) (sqlc.Item, error) {
	cur, err := q.LockItem(ctx, sqlc.LockItemParams{ID: itemID, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Item{}, apperr.NotFound("item")
	}
	if err != nil {
		return sqlc.Item{}, fmt.Errorf("lock item: %w", err)
	}
	if int(cur.Version) != version {
		return sqlc.Item{}, apperr.VersionConflict(int(cur.Version))
	}
	return cur, nil
}

// dropPrevious deletes the attachment row the item pointed to before this
// write (the item no longer references it, so the DELETE succeeds) and returns
// its file keys for deleteOld. It returns nil when there was no attachment.
func (s *Service) dropPrevious(ctx context.Context, q *sqlc.Queries, cur sqlc.Item) (*sqlc.DeleteAttachmentRow, error) {
	if !cur.ImageAttachmentID.Valid {
		return nil, nil
	}
	row, err := q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: cur.ImageAttachmentID.UUID, WorkspaceID: s.workspaceID})
	if err != nil {
		return nil, fmt.Errorf("delete previous attachment: %w", err)
	}
	return &row, nil
}

// discardUnreferenced removes the files of a failed upload. It first checks
// that the attachment row does not exist: if the commit succeeded but its
// answer was lost, the files are in use and must stay. When in doubt it leaves
// them to the sweeper.
func (s *Service) discardUnreferenced(ctx context.Context, id uuid.UUID, keys ...string) {
	ctx = context.WithoutCancel(ctx)
	_, err := sqlc.New(s.db).GetAttachment(ctx, sqlc.GetAttachmentParams{ID: id, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		s.deleteFiles(ctx, keys...)
	}
}

// deleteOld removes the files of a replaced attachment after the commit.
func (s *Service) deleteOld(ctx context.Context, old *sqlc.DeleteAttachmentRow) {
	if old != nil {
		s.deleteFiles(ctx, old.StorageKey, old.ThumbnailStorageKey)
	}
}

// deleteFiles removes files best effort. A failure is logged (no paths) and
// left to the sweeper: the files have no attachment row, so the next run
// deletes them once they are older than an hour.
func (s *Service) deleteFiles(ctx context.Context, keys ...string) {
	for _, k := range keys {
		if err := s.store.Delete(k); err != nil {
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
	row, err := sqlc.New(s.db).GetAttachment(ctx, sqlc.GetAttachmentParams{ID: id, WorkspaceID: s.workspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, apperr.NotFound("attachment")
	}
	if err != nil {
		return File{}, fmt.Errorf("get attachment: %w", err)
	}
	key, mediaType := row.StorageKey, row.MediaType
	if thumbnail {
		key, mediaType = row.ThumbnailStorageKey, "image/jpeg"
	}
	body, size, err := s.store.Open(key)
	if errors.Is(err, storage.ErrNotFound) {
		s.log.ErrorContext(ctx, "attachment row has no file", "attachment_id", id)
		return File{}, apperr.NotFound("attachment")
	}
	if err != nil {
		return File{}, fmt.Errorf("open attachment file: %w", err)
	}
	return File{Body: body, MediaType: mediaType, Size: size}, nil
}

// storageKeys builds the generated keys of an attachment: a two-level fan-out
// from the id keeps directories small. Nothing user-supplied is part of a key.
func storageKeys(id uuid.UUID, ext string) (key, thumb string) {
	s := id.String()
	dir := "images/" + s[0:2] + "/" + s[2:4] + "/" + s
	return dir + "." + ext, dir + ".thumb.jpg"
}

// cleanFilename keeps the display name: base name only, no control
// characters, at most 255 characters. nil when nothing is left.
func cleanFilename(in *string) *string {
	if in == nil {
		return nil
	}
	name := strings.NewReplacer("\\", "/").Replace(*in)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxFilenameLen {
		name = string([]rune(name)[:maxFilenameLen])
	}
	if name == "" {
		return nil
	}
	return &name
}

// cleanURL accepts absolute http and https URLs of at most 2048 characters
// (the same rule as website in the items service).
func cleanURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	bad := func(msg string) (string, error) {
		return "", apperr.Validation(apperr.FieldError{Field: "url", Message: msg})
	}
	if utf8.RuneCountInString(s) > maxURLLen {
		return bad(fmt.Sprintf("must be at most %d characters", maxURLLen))
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return bad("must be an http or https URL")
	}
	return s, nil
}
