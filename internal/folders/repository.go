package folders

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Postgres implements Store on a pool.
type Postgres struct{ db DB }

func NewPostgres(db DB) *Postgres { return &Postgres{db: db} }

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.db, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// Get reads one active folder outside a transaction.
func (p *Postgres) Get(ctx context.Context, id uuid.UUID) (Folder, error) {
	return (&repo{q: sqlc.New(p.db)}).Get(ctx, id)
}

// ListActive reads every active folder outside a transaction (the sidebar tree).
func (p *Postgres) ListActive(ctx context.Context) ([]Folder, error) {
	return (&repo{q: sqlc.New(p.db)}).ListActive(ctx)
}

// repo runs the queries of one transaction. The workspace comes from the
// context on every call (scope), never from a field.
type repo struct{ q *sqlc.Queries }

func (r *repo) LockTree(ctx context.Context) error {
	return r.q.LockTree(ctx, scope.WorkspaceID(ctx))
}

func (r *repo) Get(ctx context.Context, id uuid.UUID) (Folder, error) {
	row, err := r.q.GetFolder(ctx, sqlc.GetFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	return fromRow(row), notFound(err)
}

// GetForUpdate locks the row. It is sqlc's LockFolder, which the trash code also uses.
func (r *repo) GetForUpdate(ctx context.Context, id uuid.UUID) (Folder, error) {
	row, err := r.q.LockFolder(ctx, sqlc.LockFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	return fromRow(row), notFound(err)
}

func (r *repo) ListActive(ctx context.Context) ([]Folder, error) {
	rows, err := r.q.ListActiveFolders(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, err
	}
	out := make([]Folder, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromRow(row))
	}
	return out, nil
}

func (r *repo) ChildIDs(ctx context.Context, parent *uuid.UUID) ([]uuid.UUID, error) {
	return r.q.ListChildFolderIDs(ctx, sqlc.ListChildFolderIDsParams{WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent)})
}

func (r *repo) NextSortOrder(ctx context.Context, parent *uuid.UUID) (int, error) {
	next, err := r.q.NextFolderSortOrder(ctx, sqlc.NextFolderSortOrderParams{WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent)})
	return int(next), err
}

func (r *repo) Insert(ctx context.Context, parent *uuid.UUID, name string, sortOrder int, now time.Time) (Folder, error) {
	row, err := r.q.InsertFolder(ctx, sqlc.InsertFolderParams{
		WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent), Name: name, SortOrder: int32(sortOrder), Now: now,
	})
	return fromRow(row), err
}

func (r *repo) Rename(ctx context.Context, id uuid.UUID, name string, now time.Time) (Folder, error) {
	row, err := r.q.RenameFolder(ctx, sqlc.RenameFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Name: name, Now: now})
	return fromRow(row), err
}

func (r *repo) Move(ctx context.Context, id uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) (Folder, error) {
	row, err := r.q.MoveFolder(ctx, sqlc.MoveFolderParams{
		ID: id, WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent), SortOrder: int32(sortOrder), Now: now,
	})
	return fromRow(row), err
}

func (r *repo) SetSortOrder(ctx context.Context, id uuid.UUID, sortOrder int, now time.Time) error {
	return r.q.SetFolderSortOrder(ctx, sqlc.SetFolderSortOrderParams{
		ID: id, WorkspaceID: scope.WorkspaceID(ctx), SortOrder: int32(sortOrder), Now: now,
	})
}

// nullUUID converts an optional id (nil = root) to its sqlc form.
func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func fromRow(f sqlc.Folder) Folder {
	out := Folder{
		ID: f.ID, Name: f.Name, SortOrder: int(f.SortOrder), Version: int(f.Version),
		CreatedAt: f.CreatedAt.UTC(), UpdatedAt: f.UpdatedAt.UTC(),
	}
	if f.ParentFolderID.Valid {
		parent := f.ParentFolderID.UUID
		out.ParentID = &parent
	}
	return out
}

// notFound turns "no rows" into the domain error; other errors pass through.
func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound("folder")
	}
	return err
}
