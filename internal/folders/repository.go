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
	return r.withMembers(ctx, row, notFound(err))
}

// GetAny reads a folder in any state (leaving a share works on a trashed folder too).
func (r *repo) GetAny(ctx context.Context, id uuid.UUID) (Folder, error) {
	row, err := r.q.GetFolderAnyState(ctx, sqlc.GetFolderAnyStateParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	return r.withMembers(ctx, row, notFound(err))
}

// withMembers maps a row and loads the members of a top-level folder. err is the error of the read
// that produced row, passed through so the callers stay one line.
func (r *repo) withMembers(ctx context.Context, row sqlc.Folder, err error) (Folder, error) {
	if err != nil {
		return Folder{}, err
	}
	f := fromRow(row)
	if f.ParentID != nil {
		return f, nil
	}
	rows, err := r.q.ListFolderMembers(ctx, sqlc.ListFolderMembersParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: f.ID})
	if err != nil {
		return Folder{}, err
	}
	for _, m := range rows {
		f.MemberIDs = append(f.MemberIDs, m.UserID)
	}
	return f, nil
}

// VisibleToCaller re-reads, inside the transaction, whether the caller may see the folder (any
// state): the request-level snapshot was read before the tree lock and a share may have changed.
func (r *repo) VisibleToCaller(ctx context.Context, id uuid.UUID) (bool, error) {
	user, ok := scope.UserID(ctx)
	if !ok {
		return false, nil
	}
	return r.q.FolderVisibleTo(ctx, sqlc.FolderVisibleToParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), UserID: user})
}

func (r *repo) InsertMember(ctx context.Context, id, user uuid.UUID) error {
	return r.q.InsertFolderMember(ctx, sqlc.InsertFolderMemberParams{
		WorkspaceID: scope.WorkspaceID(ctx), FolderID: id, UserID: user, AddedByUserID: callerID(ctx),
	})
}

func (r *repo) DeleteMember(ctx context.Context, id, user uuid.UUID) (bool, error) {
	n, err := r.q.DeleteFolderMember(ctx, sqlc.DeleteFolderMemberParams{WorkspaceID: scope.WorkspaceID(ctx), FolderID: id, UserID: user})
	return n > 0, err
}

// DeleteTopOrderOf drops one user's position of a top-level folder (no foreign key, D-118).
func (r *repo) DeleteTopOrderOf(ctx context.Context, id, user uuid.UUID) error {
	return r.q.DeleteUserTopOrderEntry(ctx, sqlc.DeleteUserTopOrderEntryParams{
		WorkspaceID: scope.WorkspaceID(ctx), UserID: user, EntryType: "folder", EntryID: id,
	})
}

// GetForUpdate locks the row. It is sqlc's LockFolder, which the trash code also uses.
func (r *repo) GetForUpdate(ctx context.Context, id uuid.UUID) (Folder, error) {
	row, err := r.q.LockFolder(ctx, sqlc.LockFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx)})
	return r.withMembers(ctx, row, notFound(err))
}

func (r *repo) ListActive(ctx context.Context) ([]Folder, error) {
	rows, err := r.q.ListActiveFolders(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, err
	}
	members, err := r.q.ListAllFolderMembers(ctx, scope.WorkspaceID(ctx))
	if err != nil {
		return nil, err
	}
	byFolder := make(map[uuid.UUID][]uuid.UUID)
	for _, m := range members {
		byFolder[m.FolderID] = append(byFolder[m.FolderID], m.UserID)
	}
	out := make([]Folder, 0, len(rows))
	for _, row := range rows {
		f := fromRow(row)
		if f.ParentID == nil { // the rows of a nested folder are inert
			f.MemberIDs = byFolder[f.ID]
		}
		out = append(out, f)
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
		WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent), OwnerUserID: ownerForNew(ctx, parent),
		Name: name, SortOrder: int32(sortOrder), Now: now, CallerUserID: callerID(ctx),
	})
	return fromRow(row), err
}

func (r *repo) Rename(ctx context.Context, id uuid.UUID, name string, now time.Time) (Folder, error) {
	row, err := r.q.RenameFolder(ctx, sqlc.RenameFolderParams{ID: id, WorkspaceID: scope.WorkspaceID(ctx), Name: name, Now: now, CallerUserID: callerID(ctx)})
	return r.withMembers(ctx, row, err)
}

func (r *repo) Move(ctx context.Context, id uuid.UUID, parent *uuid.UUID, sortOrder int, now time.Time) (Folder, error) {
	row, err := r.q.MoveFolder(ctx, sqlc.MoveFolderParams{
		ID: id, WorkspaceID: scope.WorkspaceID(ctx), ParentFolderID: nullUUID(parent), CallerUserID: callerID(ctx), SortOrder: int32(sortOrder), Now: now,
	})
	if err != nil {
		return Folder{}, err
	}
	if parent != nil {
		// No longer a top-level entry: drop everyone's position of it (it has no foreign key).
		if err := r.q.DeleteUserTopOrderOf(ctx, sqlc.DeleteUserTopOrderOfParams{WorkspaceID: scope.WorkspaceID(ctx), Ids: []uuid.UUID{id}}); err != nil {
			return Folder{}, err
		}
	}
	return r.withMembers(ctx, row, nil)
}

// SetTopOrder stores the caller's own position of a top-level folder; the shared sort_order and
// other users' positions stay.
func (r *repo) SetTopOrder(ctx context.Context, id uuid.UUID, position int) error {
	user, ok := scope.UserID(ctx)
	if !ok {
		return errors.New("set top-level order: the request has no user")
	}
	return r.q.UpsertUserTopOrder(ctx, sqlc.UpsertUserTopOrderParams{
		WorkspaceID: scope.WorkspaceID(ctx), UserID: user, EntryType: "folder", EntryID: id, SortOrder: int32(position),
	})
}

// callerID is the user of the request as a nullable id (NULL without one: tests and jobs).
func callerID(ctx context.Context) uuid.NullUUID {
	id, ok := scope.UserID(ctx)
	return uuid.NullUUID{UUID: id, Valid: ok}
}

// ownerForNew is the owner of a new folder: the caller at the top level, nobody inside a folder.
func ownerForNew(ctx context.Context, parent *uuid.UUID) uuid.NullUUID {
	if parent != nil {
		return uuid.NullUUID{}
	}
	return callerID(ctx)
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
	out.CreatedByID, out.UpdatedByID = ptrNull(f.CreatedByUserID), ptrNull(f.UpdatedByUserID)
	if f.OwnerUserID.Valid {
		owner := f.OwnerUserID.UUID
		out.OwnerID = &owner
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

func ptrNull(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	return &id.UUID
}
