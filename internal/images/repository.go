package images

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Repo is the image data the service needs. Methods take the workspace from the
// context (scope). Rows map to the pure model in model.go; no rule lives here.
type Repo interface {
	// InsertAttachment stores a new attachment row created at now.
	InsertAttachment(ctx context.Context, a Attachment, now time.Time) error
	// GetAttachment returns an attachment (see Attachment for the fields it sets). NotFound when missing.
	GetAttachment(ctx context.Context, id uuid.UUID) (Attachment, error)
	// DeleteAttachment removes an attachment no item points to and returns its file keys.
	// NotFound when it is still referenced or already gone.
	DeleteAttachment(ctx context.Context, id uuid.UUID) (FileKeys, error)
	// ListAttachmentKeys returns every file key the workspace's attachments name (both files).
	ListAttachmentKeys(ctx context.Context) ([]string, error)
	// ListUnreferencedAttachments returns the ids of attachments no item points to, created before cutoff.
	ListUnreferencedAttachments(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error)

	// SEAM: moves to items. The item image columns are written here until the items feature
	// owns them. Each write bumps the item version and updated_at and works on active rows only.

	// LockItem locks an active item row (FOR UPDATE) and returns its version and image. NotFound otherwise.
	LockItem(ctx context.Context, itemID uuid.UUID) (ItemImageState, error)
	// SetItemImageAttachment points an item at an attachment and clears its URL. // SEAM: moves to items
	SetItemImageAttachment(ctx context.Context, itemID, attachmentID uuid.UUID, now time.Time) error
	// SetItemImageURL points an item at an external URL and clears its attachment. // SEAM: moves to items
	SetItemImageURL(ctx context.Context, itemID uuid.UUID, url string, now time.Time) error
	// ClearItemImage removes both the attachment and the URL of an item. // SEAM: moves to items
	ClearItemImage(ctx context.Context, itemID uuid.UUID, now time.Time) error
}

// DB is satisfied by *pgxpool.Pool.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements the repository on a pool.
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// The reads and the single-statement delete below run outside a transaction, one
// query each on the pool, with the same mapping as the Repo methods of the same name.

func (p *Postgres) reads() *repo { return &repo{q: sqlc.New(p.db)} }

func (p *Postgres) GetAttachment(ctx context.Context, id uuid.UUID) (Attachment, error) {
	return p.reads().GetAttachment(ctx, id)
}

func (p *Postgres) DeleteAttachment(ctx context.Context, id uuid.UUID) (FileKeys, error) {
	return p.reads().DeleteAttachment(ctx, id)
}

func (p *Postgres) ListAttachmentKeys(ctx context.Context) ([]string, error) {
	return p.reads().ListAttachmentKeys(ctx)
}

func (p *Postgres) ListUnreferencedAttachments(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	return p.reads().ListUnreferencedAttachments(ctx, cutoff)
}

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// repo runs the queries of one transaction.
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) InsertAttachment(ctx context.Context, a Attachment, now time.Time) error {
	err := r.q.InsertAttachment(ctx, sqlc.InsertAttachmentParams{
		ID: a.ID, WorkspaceID: scope.WorkspaceID(ctx), StorageKey: a.StorageKey, ThumbnailStorageKey: a.ThumbnailStorageKey,
		OriginalFilename: a.OriginalFilename, MediaType: a.MediaType, SizeBytes: a.SizeBytes,
		Width: int32(a.Width), Height: int32(a.Height), Sha256: a.SHA256, Now: now, //nolint:gosec // bounded by the 40 MP cap
	})
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return nil
}

func (r *repo) GetAttachment(ctx context.Context, id uuid.UUID) (Attachment, error) {
	row, err := r.q.GetAttachment(ctx, sqlc.GetAttachmentParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return Attachment{}, notFoundOr(err, "attachment")
	}
	return Attachment{
		ID: row.ID, StorageKey: row.StorageKey, ThumbnailStorageKey: row.ThumbnailStorageKey,
		MediaType: row.MediaType, SizeBytes: row.SizeBytes,
	}, nil
}

func (r *repo) DeleteAttachment(ctx context.Context, id uuid.UUID) (FileKeys, error) {
	row, err := r.q.DeleteAttachment(ctx, sqlc.DeleteAttachmentParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return FileKeys{}, notFoundOr(err, "attachment")
	}
	return FileKeys{StorageKey: row.StorageKey, ThumbnailStorageKey: row.ThumbnailStorageKey}, nil
}

func (r *repo) ListAttachmentKeys(ctx context.Context) ([]string, error) {
	keys, err := r.q.ListAttachmentKeys(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, fmt.Errorf("list attachment keys: %w", err)
	}
	return keys, nil
}

func (r *repo) ListUnreferencedAttachments(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	ids, err := r.q.ListUnreferencedAttachments(ctx, sqlc.ListUnreferencedAttachmentsParams{WorkspaceID: scope.WorkspaceID(ctx), Cutoff: cutoff})
	if err != nil {
		return nil, fmt.Errorf("list unreferenced attachments: %w", err)
	}
	return ids, nil
}

// LockItem is SEAM: moves to items.
func (r *repo) LockItem(ctx context.Context, itemID uuid.UUID) (ItemImageState, error) {
	row, err := r.q.LockItem(ctx, sqlc.LockItemParams{ID: itemID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return ItemImageState{}, notFoundOr(err, "item")
	}
	st := ItemImageState{Version: int(row.Version), URL: row.ImageUrl}
	if row.ImageAttachmentID.Valid {
		id := row.ImageAttachmentID.UUID
		st.AttachmentID = &id
	}
	return st, nil
}

// SetItemImageAttachment is SEAM: moves to items.
func (r *repo) SetItemImageAttachment(ctx context.Context, itemID, attachmentID uuid.UUID, now time.Time) error {
	if err := r.q.SetItemImageAttachment(ctx, sqlc.SetItemImageAttachmentParams{
		AttachmentID: uuid.NullUUID{UUID: attachmentID, Valid: true}, Now: now, ID: itemID, WorkspaceID: scope.WorkspaceID(ctx), CallerUserID: callerID(ctx),
	}); err != nil {
		return fmt.Errorf("set item image: %w", err)
	}
	return nil
}

// SetItemImageURL is SEAM: moves to items.
func (r *repo) SetItemImageURL(ctx context.Context, itemID uuid.UUID, url string, now time.Time) error {
	if err := r.q.SetItemImageURL(ctx, sqlc.SetItemImageURLParams{ImageUrl: &url, Now: now, ID: itemID, WorkspaceID: scope.WorkspaceID(ctx), CallerUserID: callerID(ctx)}); err != nil {
		return fmt.Errorf("set item image url: %w", err)
	}
	return nil
}

// ClearItemImage is SEAM: moves to items.
func (r *repo) ClearItemImage(ctx context.Context, itemID uuid.UUID, now time.Time) error {
	if err := r.q.ClearItemImage(ctx, sqlc.ClearItemImageParams{Now: now, ID: itemID, WorkspaceID: scope.WorkspaceID(ctx), CallerUserID: callerID(ctx)}); err != nil {
		return fmt.Errorf("clear item image: %w", err)
	}
	return nil
}

// notFoundOr maps a missing row to apperr.NotFound and wraps any other error.
func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("%s: %w", resource, err)
}

// callerID is the user of the request as a nullable id (NULL for jobs and tests); NULL keeps the
// previous updated-by value on updates.
func callerID(ctx context.Context) uuid.NullUUID {
	id, ok := scope.UserID(ctx)
	return uuid.NullUUID{UUID: id, Valid: ok}
}
