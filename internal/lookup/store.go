package lookup

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/images"
	"github.com/0xfederama/collections-app-backend/internal/items"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
	"github.com/0xfederama/collections-app-backend/internal/storage"
)

// itemReader reads the item after a lookup commits (*items.Service in production).
type itemReader interface {
	Get(ctx context.Context, id uuid.UUID) (items.Item, error)
}

// DBStore applies one fetched lookup result to item data and its provenance.
// Its methods scope the context to the workspace, so callers need not.
type DBStore struct {
	repo        Repo
	store       storage.FileStore
	clock       clock.Clock
	items       itemReader
	log         *slog.Logger
	workspaceID uuid.UUID
}

var _ Store = (*DBStore)(nil)

// NewStore returns a lookup store bound to one workspace.
func NewStore(db DB, fileStore storage.FileStore, clk clock.Clock, itemSvc *items.Service, log *slog.Logger, workspaceID uuid.UUID) *DBStore {
	return newStore(NewPostgres(db), fileStore, clk, itemSvc, log, workspaceID)
}

func newStore(repo Repo, fileStore storage.FileStore, clk clock.Clock, itemSvc itemReader, log *slog.Logger, workspaceID uuid.UUID) *DBStore {
	return &DBStore{repo: repo, store: fileStore, clock: clk, items: itemSvc, log: log, workspaceID: workspaceID}
}

// ListType returns the type of an active list, "" for a list with no type.
func (s *DBStore) ListType(ctx context.Context, listID uuid.UUID) (string, error) {
	typ, err := s.repo.ListType(scope.With(ctx, s.workspaceID), listID)
	if err != nil {
		return "", err
	}
	return typeKey(typ), nil
}

// Read returns the current provenance record, including the item's current
// version. A missing record is not an error.
func (s *DBStore) Read(ctx context.Context, itemID uuid.UUID) (Record, bool, error) {
	return s.repo.Import(scope.With(ctx, s.workspaceID), itemID)
}

type preparedPoster struct {
	id            uuid.UUID
	key, thumbKey string
	processed     *images.Processed
}

func (s *DBStore) preparePoster(p *images.Processed) (*preparedPoster, error) {
	if p == nil {
		return nil, nil
	}
	id := uuid.New()
	key, thumb := lookupStorageKeys(id, p.Ext)
	if _, err := s.store.Put(key, bytes.NewReader(p.Original)); err != nil {
		return nil, fmt.Errorf("store lookup poster: %w", err)
	}
	if _, err := s.store.Put(thumb, bytes.NewReader(p.Thumbnail)); err != nil {
		s.deleteFiles(context.Background(), key)
		return nil, fmt.Errorf("store lookup poster thumbnail: %w", err)
	}
	return &preparedPoster{id: id, key: key, thumbKey: thumb, processed: p}, nil
}

// insertAttachment writes the poster's attachment row inside the transaction.
func (s *DBStore) insertAttachment(ctx context.Context, r Repo, p *preparedPoster) error {
	img := p.processed
	sum := img.SHA256
	if err := r.InsertAttachment(ctx, images.Attachment{
		ID: p.id, StorageKey: p.key, ThumbnailStorageKey: p.thumbKey, MediaType: img.MediaType,
		SizeBytes: int64(len(img.Original)), Width: img.Width, Height: img.Height, SHA256: sum[:],
	}, s.clock.Now()); err != nil {
		return fmt.Errorf("insert lookup attachment: %w", err)
	}
	return nil
}

// discardPrepared protects the ambiguous-commit case: only remove prepared
// files after confirming no committed attachment row owns them. Any read error
// leaves them for the orphan sweeper.
func (s *DBStore) discardPrepared(ctx context.Context, p *preparedPoster) {
	exists, err := s.repo.AttachmentExists(ctx, p.id)
	if err == nil && !exists {
		s.deleteFiles(ctx, p.key, p.thumbKey)
	}
}

func (s *DBStore) deleteFiles(ctx context.Context, keys ...string) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := s.store.Delete(key); err != nil {
			s.log.WarnContext(ctx, "lookup image cleanup failed; the sweeper will retry", "err", err)
		}
	}
}
