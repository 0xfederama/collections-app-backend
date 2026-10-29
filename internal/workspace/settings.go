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
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
	"github.com/0xfederama/collections-app-backend/internal/stores"
)

// Settings is the workspace name plus the workspace_settings row. Version is
// workspace_settings.version, the single ETag stream "s-N" (API.md §3).
// TrashRetentionDays nil means off: the trash is never purged automatically (F012-B3).
// PreferredStore is the one default store key (F-016, 0.13.0); never empty.
type Settings struct {
	WorkspaceName        string
	StreamingCountryCode string
	MetadataLanguage     string
	MarkdownHistoryLimit int
	TrashRetentionDays   *int
	PreferredStore       string
	Version              int
}

// SettingsPatch holds the fields a PATCH supplied; nil means "leave as is".
// TrashRetentionDays is an Optional: absent leaves it, an explicit null turns it off.
type SettingsPatch struct {
	WorkspaceName        *string
	StreamingCountryCode *string
	MetadataLanguage     *string
	MarkdownHistoryLimit *int
	TrashRetentionDays   Optional[int]
	PreferredStore       *string
}

// Optional is a PATCH field that can be cleared: Set tells absent from present,
// and a nil Value means an explicit null.
type Optional[T any] struct {
	Set   bool
	Value *T
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
		if next.equal(cur) {
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
			MetadataLanguage:     next.MetadataLanguage,
			MarkdownHistoryLimit: int32(next.MarkdownHistoryLimit),
			TrashRetentionDays:   retentionToDB(next.TrashRetentionDays),
			PreferredStore:       next.PreferredStore,
		})
		if err != nil {
			return fmt.Errorf("update settings: %w", err)
		}
		if next.StreamingCountryCode != cur.StreamingCountryCode {
			// The imported JustWatch link belongs to the old country.
			if err := q.ClearItemStreamingLinks(ctx, workspaceID); err != nil {
				return fmt.Errorf("clear item streaming links: %w", err)
			}
			if err := q.ClearStreamingLinkSnapshots(ctx, workspaceID); err != nil {
				return fmt.Errorf("clear streaming link snapshots: %w", err)
			}
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
	if p.MetadataLanguage != nil && !isMetadataLanguage(*p.MetadataLanguage) {
		errs = append(errs, apperr.FieldError{Field: "metadata_language", Message: "must be an ISO 639-1 code (xx) optionally followed by an uppercase region (xx-YY)"})
	}
	if p.MarkdownHistoryLimit != nil && (*p.MarkdownHistoryLimit < 0 || *p.MarkdownHistoryLimit > 200) {
		errs = append(errs, apperr.FieldError{Field: "markdown_history_limit", Message: "must be between 0 and 200"})
	}
	if p.TrashRetentionDays.Value != nil && (*p.TrashRetentionDays.Value < 1 || *p.TrashRetentionDays.Value > 365) {
		errs = append(errs, apperr.FieldError{Field: "trash_retention_days", Message: "must be between 1 and 365, or null to turn it off"})
	}
	if p.PreferredStore != nil && !stores.Valid(*p.PreferredStore) {
		errs = append(errs, apperr.FieldError{Field: "preferred_store", Message: fmt.Sprintf("unknown store %q", *p.PreferredStore)})
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

func isMetadataLanguage(s string) bool {
	if len(s) != 2 && len(s) != 5 {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' || s[1] < 'a' || s[1] > 'z' {
		return false
	}
	return len(s) == 2 || (s[2] == '-' && isUpperAlpha(s[3:], 2))
}

func (p SettingsPatch) applyTo(s Settings) Settings {
	if p.WorkspaceName != nil {
		s.WorkspaceName = *p.WorkspaceName
	}
	if p.StreamingCountryCode != nil {
		s.StreamingCountryCode = *p.StreamingCountryCode
	}
	if p.MetadataLanguage != nil {
		s.MetadataLanguage = *p.MetadataLanguage
	}
	if p.MarkdownHistoryLimit != nil {
		s.MarkdownHistoryLimit = *p.MarkdownHistoryLimit
	}
	if p.TrashRetentionDays.Set {
		s.TrashRetentionDays = p.TrashRetentionDays.Value
	}
	if p.PreferredStore != nil {
		s.PreferredStore = *p.PreferredStore
	}
	return s
}

// equal reports whether two settings hold the same values. The retention is a
// pointer, so it is compared by value: a PATCH that repeats the stored value changes nothing.
func (s Settings) equal(o Settings) bool {
	return s.WorkspaceName == o.WorkspaceName && s.StreamingCountryCode == o.StreamingCountryCode &&
		s.MetadataLanguage == o.MetadataLanguage && s.MarkdownHistoryLimit == o.MarkdownHistoryLimit && sameRetention(s.TrashRetentionDays, o.TrashRetentionDays) &&
		s.PreferredStore == o.PreferredStore
}

func sameRetention(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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
