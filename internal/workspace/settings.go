package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/database"
	"github.com/0xfederama/collections-app-backend/internal/database/sqlc"
)

// Settings is the workspace name plus the workspace_settings row. Version is
// workspace_settings.version, the single ETag stream "s-N" (API.md §3).
type Settings struct {
	WorkspaceName        string
	StreamingCountryCode string
	DefaultCurrency      string
	MarkdownHistoryLimit int
	Version              int
}

// SettingsPatch holds the fields a PATCH supplied; nil means "leave as is".
type SettingsPatch struct {
	WorkspaceName        *string
	StreamingCountryCode *string
	DefaultCurrency      *string
	MarkdownHistoryLimit *int
}

// Service reads and updates the settings of the workspace.
type Service struct {
	db *pgxpool.Pool
}

// NewService returns a Service on the pool.
func NewService(db *pgxpool.Pool) *Service { return &Service{db: db} }

// Get returns the current settings.
func (s *Service) Get(ctx context.Context, workspaceID uuid.UUID) (Settings, error) {
	row, err := sqlc.New(s.db).GetSettings(ctx, workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, apperr.NotFound("settings")
	}
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	return fromGetRow(row), nil
}

// Update applies the patch if ifMatch is the current version. It validates the
// fields first (422), then locks the settings row so concurrent PATCHes are
// serialised, and compares the version (412 with the current one). A patch that
// changes nothing returns the current settings without bumping the version.
// A name change updates workspaces.name in the same transaction and bumps the
// same version.
func (s *Service) Update(ctx context.Context, workspaceID uuid.UUID, ifMatch int, p SettingsPatch) (Settings, error) {
	p, err := p.validate()
	if err != nil {
		return Settings{}, err
	}

	var out Settings
	err = database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		row, err := q.LockSettings(ctx, workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("settings")
		}
		if err != nil {
			return fmt.Errorf("lock settings: %w", err)
		}
		cur := fromLockRow(row)
		if cur.Version != ifMatch {
			return apperr.VersionConflict(cur.Version)
		}

		next := p.applyTo(cur)
		if next == cur {
			out = cur
			return nil
		}
		if next.WorkspaceName != cur.WorkspaceName {
			if err := q.UpdateWorkspaceName(ctx, sqlc.UpdateWorkspaceNameParams{ID: workspaceID, Name: next.WorkspaceName}); err != nil {
				return fmt.Errorf("update workspace name: %w", err)
			}
		}
		err = q.UpdateSettings(ctx, sqlc.UpdateSettingsParams{
			WorkspaceID:          workspaceID,
			StreamingCountryCode: next.StreamingCountryCode,
			DefaultCurrency:      next.DefaultCurrency,
			MarkdownHistoryLimit: int32(next.MarkdownHistoryLimit),
		})
		if err != nil {
			return fmt.Errorf("update settings: %w", err)
		}
		next.Version = cur.Version + 1
		out = next
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}

// validate trims the name and checks every supplied field, collecting all failures.
func (p SettingsPatch) validate() (SettingsPatch, error) {
	var errs []apperr.FieldError
	if p.WorkspaceName != nil {
		name := strings.TrimSpace(*p.WorkspaceName)
		p.WorkspaceName = &name
		if n := utf8.RuneCountInString(name); n < 1 || n > 200 {
			errs = append(errs, apperr.FieldError{Field: "workspace_name", Message: "must be 1 to 200 characters"})
		}
	}
	if p.StreamingCountryCode != nil && !isUpperAlpha(*p.StreamingCountryCode, 2) {
		errs = append(errs, apperr.FieldError{Field: "streaming_country_code", Message: "must be 2 uppercase letters (ISO 3166-1 alpha-2)"})
	}
	if p.DefaultCurrency != nil && !isUpperAlpha(*p.DefaultCurrency, 3) {
		errs = append(errs, apperr.FieldError{Field: "default_currency", Message: "must be 3 uppercase letters (ISO 4217)"})
	}
	if p.MarkdownHistoryLimit != nil && (*p.MarkdownHistoryLimit < 0 || *p.MarkdownHistoryLimit > 200) {
		errs = append(errs, apperr.FieldError{Field: "markdown_history_limit", Message: "must be between 0 and 200"})
	}
	if len(errs) > 0 {
		return p, apperr.Validation(errs...)
	}
	return p, nil
}

// isUpperAlpha reports whether s is exactly n ASCII letters A-Z (the DB CHECK regex).
func isUpperAlpha(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

func (p SettingsPatch) applyTo(s Settings) Settings {
	if p.WorkspaceName != nil {
		s.WorkspaceName = *p.WorkspaceName
	}
	if p.StreamingCountryCode != nil {
		s.StreamingCountryCode = *p.StreamingCountryCode
	}
	if p.DefaultCurrency != nil {
		s.DefaultCurrency = *p.DefaultCurrency
	}
	if p.MarkdownHistoryLimit != nil {
		s.MarkdownHistoryLimit = *p.MarkdownHistoryLimit
	}
	return s
}

func fromGetRow(r sqlc.GetSettingsRow) Settings {
	return Settings{r.WorkspaceName, r.StreamingCountryCode, r.DefaultCurrency, int(r.MarkdownHistoryLimit), int(r.Version)}
}

func fromLockRow(r sqlc.LockSettingsRow) Settings {
	return Settings{r.WorkspaceName, r.StreamingCountryCode, r.DefaultCurrency, int(r.MarkdownHistoryLimit), int(r.Version)}
}
