package users

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Postgres implements Store on a pool. The workspace comes from the context.
type Postgres struct{ q *sqlc.Queries }

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{q: sqlc.New(pool)} }

func (p *Postgres) ByEmail(ctx context.Context, email string) (User, error) {
	row, err := p.q.GetUserByEmail(ctx, sqlc.GetUserByEmailParams{WorkspaceID: scope.WorkspaceID(ctx), Email: email})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, apperr.NotFound("user")
	}
	if err != nil {
		return User{}, fmt.Errorf("get user by email: %w", err)
	}
	return fromRow(row), nil
}

func (p *Postgres) Upsert(ctx context.Context, email string) (User, error) {
	row, err := p.q.UpsertUserByEmail(ctx, sqlc.UpsertUserByEmailParams{WorkspaceID: scope.WorkspaceID(ctx), Email: email})
	if err != nil {
		return User{}, fmt.Errorf("upsert user: %w", err)
	}
	return fromRow(row), nil
}

func (p *Postgres) Get(ctx context.Context, id uuid.UUID) (User, error) {
	row, err := p.q.GetUser(ctx, sqlc.GetUserParams{WorkspaceID: scope.WorkspaceID(ctx), ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, apperr.NotFound("user")
	}
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	return fromRow(row), nil
}

func (p *Postgres) UpdateProfile(ctx context.Context, id uuid.UUID, pr Profile, at time.Time) (User, error) {
	row, err := p.q.UpdateUserProfile(ctx, sqlc.UpdateUserProfileParams{
		WorkspaceID: scope.WorkspaceID(ctx), ID: id, Name: pr.Name, AvatarUrl: pr.AvatarURL, ProfileFetchedAt: &at,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, apperr.NotFound("user")
	}
	if err != nil {
		return User{}, fmt.Errorf("update user profile: %w", err)
	}
	return fromRow(row), nil
}

func fromRow(r sqlc.User) User {
	return User{ID: r.ID, Email: r.Email, Name: r.Name, AvatarURL: r.AvatarUrl, ProfileFetchedAt: r.ProfileFetchedAt}
}
