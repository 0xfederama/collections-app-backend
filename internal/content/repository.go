package content

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

// DB is what the repository runs on: a pool or a connection, and a transaction starter.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements the repository on a pool.
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// repo runs the queries of one transaction. Queries that filter by workspace
// read it from the context (scope), never from a field.
type repo struct{ q *sqlc.Queries }

var _ Repo = (*repo)(nil)

func (r *repo) ActiveItem(ctx context.Context, itemID uuid.UUID) error {
	_, err := r.q.ItemIsActive(ctx, sqlc.ItemIsActiveParams{ID: itemID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return notFound(err, "item")
	}
	return nil
}

func (r *repo) LockItem(ctx context.Context, itemID uuid.UUID) error {
	_, err := r.q.LockItemForContent(ctx, sqlc.LockItemForContentParams{ID: itemID, WorkspaceID: scope.WorkspaceID(ctx)})
	if err != nil {
		return notFound(err, "item")
	}
	return nil
}

// Current reads the highest version; none is version 0.
func (r *repo) Current(ctx context.Context, itemID uuid.UUID) (Stored, error) {
	row, err := r.q.GetLatestContentVersion(ctx, itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Stored{Content: Content{ItemID: itemID}}, nil
	}
	if err != nil {
		return Stored{}, fmt.Errorf("get latest content version: %w", err)
	}
	return storedOf(itemID, row.Version, row.Body, row.Sha256, row.CreatedAt, row.AuthorUserID), nil
}

func (r *repo) GetVersion(ctx context.Context, itemID uuid.UUID, version int) (Stored, error) {
	row, err := r.q.GetContentVersion(ctx, sqlc.GetContentVersionParams{ItemID: itemID, Version: int32(version)})
	if err != nil {
		return Stored{}, notFound(err, "content version")
	}
	return storedOf(itemID, row.Version, row.Body, row.Sha256, row.CreatedAt, row.AuthorUserID), nil
}

// ListVersions returns the saved versions, newest first.
func (r *repo) ListVersions(ctx context.Context, itemID uuid.UUID) ([]VersionInfo, error) {
	rows, err := r.q.ListContentVersions(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("list content versions: %w", err)
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, VersionInfo{Version: int(row.Version), SizeBytes: int(row.SizeBytes), CreatedAt: row.CreatedAt, AuthorUserID: ptrNull(row.AuthorUserID)})
	}
	return out, nil
}

func (r *repo) InsertVersion(ctx context.Context, itemID uuid.UUID, version int, body string, sum []byte, now time.Time) error {
	if err := r.q.InsertContentVersion(ctx, sqlc.InsertContentVersionParams{
		ItemID: itemID, Version: int32(version), Body: body, SizeBytes: int32(len(body)), Sha256: sum, CreatedAt: now, AuthorUserID: callerID(ctx),
	}); err != nil {
		return fmt.Errorf("insert content version: %w", err)
	}
	return nil
}

// PruneVersions deletes every version up to and including upTo.
func (r *repo) PruneVersions(ctx context.Context, itemID uuid.UUID, upTo int) error {
	if err := r.q.PruneContentVersions(ctx, sqlc.PruneContentVersionsParams{ItemID: itemID, UpTo: int32(upTo)}); err != nil {
		return fmt.Errorf("prune content versions: %w", err)
	}
	return nil
}

func (r *repo) TouchItem(ctx context.Context, itemID uuid.UUID, now time.Time) error {
	if err := r.q.TouchItemUpdatedAt(ctx, sqlc.TouchItemUpdatedAtParams{ID: itemID, WorkspaceID: scope.WorkspaceID(ctx), Now: now, CallerUserID: callerID(ctx)}); err != nil {
		return fmt.Errorf("touch item: %w", err)
	}
	return nil
}

// storedOf maps a saved version row. The item id comes from the caller.
func storedOf(itemID uuid.UUID, version int32, body string, sum []byte, createdAt time.Time, author uuid.NullUUID) Stored {
	return Stored{
		Content: Content{ItemID: itemID, Version: int(version), Body: body, UpdatedAt: &createdAt, AuthorUserID: ptrNull(author)},
		SHA256:  sum,
	}
}

func notFound(err error, resource string) error {
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

func ptrNull(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	return &id.UUID
}
