package workspace

import (
	"context"
	"fmt"
	"slices"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/stores"
)

// Store opens transactions and reads the settings. Declared here, where it is
// used; *Postgres implements it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
	Get(ctx context.Context) (Settings, error)
}

// Repo is the set of queries one transaction may run. Every method is one
// plain query on the workspace of the context; the rules live in Service.
type Repo interface {
	LockSettings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, s Settings) error
	ClearItemStreamingLinks(ctx context.Context) error
	ClearStreamingLinkSnapshots(ctx context.Context) error
}

// Service reads and updates the settings of the workspace.
type Service struct {
	store Store
}

// NewService returns a Service on the store.
func NewService(store Store) *Service { return &Service{store: store} }

// Get returns the current settings of the workspace of ctx.
func (s *Service) Get(ctx context.Context) (Settings, error) { return s.store.Get(ctx) }

// Update applies the patch if ifMatch is the current version. It validates the
// fields first (422), then locks the settings row so concurrent PATCHes are
// serialised, and compares the version (412 with the current one). A patch that
// changes nothing returns the current settings without bumping the version.
func (s *Service) Update(ctx context.Context, ifMatch int, p SettingsPatch) (Settings, error) {
	p, err := p.validate()
	if err != nil {
		return Settings{}, err
	}

	var out Settings
	err = s.store.InTx(ctx, func(r Repo) error {
		cur, err := r.LockSettings(ctx)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return apperr.VersionConflict(cur.Version)
		}

		next := p.applyTo(cur)
		if next.equal(cur) {
			out = cur
			return nil
		}
		if err := r.UpdateSettings(ctx, next); err != nil {
			return err
		}
		if next.StreamingCountryCode != cur.StreamingCountryCode {
			// The imported JustWatch link belongs to the old country.
			if err := r.ClearItemStreamingLinks(ctx); err != nil {
				return err
			}
			if err := r.ClearStreamingLinkSnapshots(ctx); err != nil {
				return err
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

// validate checks every supplied field, collecting all failures.
func (p SettingsPatch) validate() (SettingsPatch, error) {
	var errs []apperr.FieldError
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
	if p.OwnedStreamingServices != nil {
		if msg := checkStreamingKeys(*p.OwnedStreamingServices); msg != "" {
			errs = append(errs, apperr.FieldError{Field: "owned_streaming_services", Message: msg})
		}
	}
	if len(errs) > 0 {
		return p, apperr.Validation(errs...)
	}
	return p, nil
}

// maxOwnedStreamingServices bounds the list; the DB CHECK uses the same number.
const maxOwnedStreamingServices = 64

// checkStreamingKeys returns the first rule the owned-services list breaks, or
// "". The web registry owns the list of keys, so the rule here is only the
// shape: lowercase letters, digits and underscores, starting with a letter.
// Duplicates are rejected, not dropped, so the client sees what it sent.
func checkStreamingKeys(keys []string) string {
	if len(keys) > maxOwnedStreamingServices {
		return fmt.Sprintf("must have at most %d entries", maxOwnedStreamingServices)
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !isStreamingKey(key) {
			return "each service key must be 1 to 64 lowercase letters, digits or underscores, starting with a letter"
		}
		if seen[key] {
			return fmt.Sprintf("duplicate service key %q", key) // key is valid here, so at most 64 chars
		}
		seen[key] = true
	}
	return ""
}

func isStreamingKey(s string) bool {
	if len(s) < 1 || len(s) > 64 || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
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
	if p.OwnedStreamingServices != nil {
		s.OwnedStreamingServices = slices.Clone(*p.OwnedStreamingServices)
	}
	return s
}

// equal reports whether two settings hold the same values. The retention is a
// pointer, so it is compared by value: a PATCH that repeats the stored value changes nothing.
func (s Settings) equal(o Settings) bool {
	return s.StreamingCountryCode == o.StreamingCountryCode &&
		s.MetadataLanguage == o.MetadataLanguage && s.MarkdownHistoryLimit == o.MarkdownHistoryLimit && sameRetention(s.TrashRetentionDays, o.TrashRetentionDays) &&
		s.PreferredStore == o.PreferredStore && slices.Equal(s.OwnedStreamingServices, o.OwnedStreamingServices)
}

func sameRetention(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
