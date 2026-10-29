package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Postgres implements Store on a pool.
type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// InTx runs fn in one transaction: commit on nil, rollback on error.
func (p *Postgres) InTx(ctx context.Context, fn func(Repo) error) error {
	return database.InTx(ctx, p.pool, func(q *sqlc.Queries) error {
		return fn(&repo{q: q})
	})
}

// Get reads the settings outside a transaction.
func (p *Postgres) Get(ctx context.Context) (Settings, error) {
	return (&repo{q: sqlc.New(p.pool)}).Get(ctx)
}

// repo runs the queries of one transaction. The workspace comes from the
// context on every call (scope), never from a field.
type repo struct{ q *sqlc.Queries }

func (r *repo) Get(ctx context.Context) (Settings, error) {
	row, err := r.q.GetSettings(ctx, scope.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, apperr.NotFound("settings")
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return fromGetRow(row), nil
}

func (r *repo) LockSettings(ctx context.Context) (Settings, error) {
	row, err := r.q.LockSettings(ctx, scope.WorkspaceID(ctx))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, apperr.NotFound("settings")
	}
	if err != nil {
		return Settings{}, fmt.Errorf("lock settings: %w", err)
	}
	return fromLockRow(row), nil
}

func (r *repo) UpdateWorkspaceName(ctx context.Context, name string) error {
	if err := r.q.UpdateWorkspaceName(ctx, sqlc.UpdateWorkspaceNameParams{ID: scope.WorkspaceID(ctx), Name: name}); err != nil {
		return fmt.Errorf("update workspace name: %w", err)
	}
	return nil
}

func (r *repo) UpdateSettings(ctx context.Context, s Settings) error {
	err := r.q.UpdateSettings(ctx, sqlc.UpdateSettingsParams{
		WorkspaceID:          scope.WorkspaceID(ctx),
		StreamingCountryCode: s.StreamingCountryCode,
		MetadataLanguage:     s.MetadataLanguage,
		MarkdownHistoryLimit: int32(s.MarkdownHistoryLimit),
		TrashRetentionDays:   retentionToDB(s.TrashRetentionDays),
		PreferredStore:       s.PreferredStore,
	})
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}

func (r *repo) ClearItemStreamingLinks(ctx context.Context) error {
	if err := r.q.ClearItemStreamingLinks(ctx, scope.WorkspaceID(ctx)); err != nil {
		return fmt.Errorf("clear item streaming links: %w", err)
	}
	return nil
}

func (r *repo) ClearStreamingLinkSnapshots(ctx context.Context) error {
	if err := r.q.ClearStreamingLinkSnapshots(ctx, scope.WorkspaceID(ctx)); err != nil {
		return fmt.Errorf("clear streaming link snapshots: %w", err)
	}
	return nil
}

// retentionToDB maps the nullable retention to the column (NULL = off).
func retentionToDB(days *int) *int32 {
	if days == nil {
		return nil
	}
	v := int32(*days)
	return &v
}

// retentionFromDB maps the column back to the nullable retention.
func retentionFromDB(days *int32) *int {
	if days == nil {
		return nil
	}
	v := int(*days)
	return &v
}

func fromGetRow(r sqlc.GetSettingsRow) Settings {
	return Settings{
		WorkspaceName:        r.WorkspaceName,
		StreamingCountryCode: r.StreamingCountryCode,
		MetadataLanguage:     r.MetadataLanguage,
		MarkdownHistoryLimit: int(r.MarkdownHistoryLimit),
		TrashRetentionDays:   retentionFromDB(r.TrashRetentionDays),
		PreferredStore:       r.PreferredStore,
		Version:              int(r.Version),
	}
}

func fromLockRow(r sqlc.LockSettingsRow) Settings {
	return Settings{
		WorkspaceName:        r.WorkspaceName,
		StreamingCountryCode: r.StreamingCountryCode,
		MetadataLanguage:     r.MetadataLanguage,
		MarkdownHistoryLimit: int(r.MarkdownHistoryLimit),
		TrashRetentionDays:   retentionFromDB(r.TrashRetentionDays),
		PreferredStore:       r.PreferredStore,
		Version:              int(r.Version),
	}
}
