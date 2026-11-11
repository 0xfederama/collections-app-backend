package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/scope"
)

// Store opens transactions. Declared here, where it is used; *Postgres implements
// it and tests use a fake.
type Store interface {
	InTx(ctx context.Context, fn func(Repo) error) error
}

var _ Store = (*Postgres)(nil)

// SettingsSource reads markdown_history_limit. The composition root wires it over
// the workspace settings. It is read before the transaction, never inside it.
type SettingsSource interface {
	MarkdownHistoryLimit(ctx context.Context) (int, error)
}

// Repo is the set of queries one transaction may run. Every method is one plain
// query; the rules live in Service. Methods read the workspace from the context
// where a query filters by it. Lookups of a missing row are apperr.NotFound.
type Repo interface {
	// ActiveItem is NotFound for a missing or trashed item.
	ActiveItem(ctx context.Context, itemID uuid.UUID) error
	// LockItem locks the item row (FOR NO KEY UPDATE) until the transaction ends. NotFound otherwise.
	LockItem(ctx context.Context, itemID uuid.UUID) error
	// Current returns the highest version of the item; version 0 when none was saved.
	Current(ctx context.Context, itemID uuid.UUID) (Stored, error)
	GetVersion(ctx context.Context, itemID uuid.UUID, version int) (Stored, error)
	// ListVersions returns the saved versions of the item, newest first.
	ListVersions(ctx context.Context, itemID uuid.UUID) ([]VersionInfo, error)
	InsertVersion(ctx context.Context, itemID uuid.UUID, version int, body string, sum []byte, now time.Time) error
	// PruneVersions deletes the versions up to and including upTo.
	PruneVersions(ctx context.Context, itemID uuid.UUID, upTo int) error
	// TouchItem bumps the item's updated_at, not its version.
	TouchItem(ctx context.Context, itemID uuid.UUID, now time.Time) error
}

// Service is the content service. Save and restore run in one transaction that
// locks the item row before it compares the version the client sent.
type Service struct {
	store    Store
	settings SettingsSource
	clock    clock.Clock
}

// NewService returns the content service over store.
func NewService(store Store, settings SettingsSource, clk clock.Clock) *Service {
	return &Service{store: store, settings: settings, clock: clk}
}

// Get returns the current content of an active item.
func (s *Service) Get(ctx context.Context, itemID uuid.UUID) (Content, error) {
	var out Content
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.ActiveItem(ctx, itemID); err != nil {
			return err
		}
		cur, err := r.Current(ctx, itemID)
		if err != nil {
			return err
		}
		out = cur.Content
		return nil
	})
	if err != nil {
		return Content{}, err
	}
	return out, nil
}

// Version returns one saved version (404 if it never existed or was pruned).
func (s *Service) Version(ctx context.Context, itemID uuid.UUID, version int) (Content, error) {
	var out Content
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.ActiveItem(ctx, itemID); err != nil {
			return err
		}
		v, err := r.GetVersion(ctx, itemID, version)
		if err != nil {
			return err
		}
		out = v.Content
		return nil
	})
	if err != nil {
		return Content{}, err
	}
	return out, nil
}

// Versions lists the saved versions, newest first.
func (s *Service) Versions(ctx context.Context, itemID uuid.UUID) ([]VersionInfo, error) {
	var out []VersionInfo
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.ActiveItem(ctx, itemID); err != nil {
			return err
		}
		vs, err := r.ListVersions(ctx, itemID)
		if err != nil {
			return err
		}
		out = vs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Save stores body as the next version if ifMatch is the current version.
// Errors in order: 413 and 422 for the body, NotFound, VersionConflict. A body
// equal to the current one creates no version and returns it unchanged.
func (s *Service) Save(ctx context.Context, itemID uuid.UUID, ifMatch int, body string) (Content, error) {
	if err := validateBody(body); err != nil {
		return Content{}, err
	}
	limit, err := s.historyLimit(ctx)
	if err != nil {
		return Content{}, err
	}
	return s.write(ctx, itemID, ifMatch, limit, func(Repo) (string, error) { return body, nil })
}

// Restore saves the body of an older version as a new version if ifMatch is
// the current version. A pruned or unknown version is a NotFound.
func (s *Service) Restore(ctx context.Context, itemID uuid.UUID, ifMatch, version int) (Content, error) {
	limit, err := s.historyLimit(ctx)
	if err != nil {
		return Content{}, err
	}
	return s.write(ctx, itemID, ifMatch, limit, func(r Repo) (string, error) {
		v, err := r.GetVersion(ctx, itemID, version)
		if err != nil {
			return "", err
		}
		return v.Body, nil
	})
}

// historyLimit reads the retention limit before the transaction opens.
func (s *Service) historyLimit(ctx context.Context) (int, error) {
	limit, err := s.settings.MarkdownHistoryLimit(ctx)
	if err != nil {
		return 0, fmt.Errorf("get markdown history limit: %w", err)
	}
	return limit, nil
}

// write is the one save path: lock the item, compare the version, insert the
// next version unless the body is unchanged, prune, touch updated_at. body runs
// inside the transaction (restore reads the old version there).
func (s *Service) write(ctx context.Context, itemID uuid.UUID, ifMatch, limit int, body func(Repo) (string, error)) (Content, error) {
	var out Content
	err := s.store.InTx(ctx, func(r Repo) error {
		if err := r.LockItem(ctx, itemID); err != nil {
			return err
		}
		cur, err := r.Current(ctx, itemID)
		if err != nil {
			return err
		}
		if cur.Version != ifMatch {
			return apperr.VersionConflict(cur.Version)
		}
		next, err := body(r)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(next))
		if cur.Version == 0 && next == "" {
			out = Content{ItemID: itemID} // version 0 already is the empty content
			return nil
		}
		if cur.Version > 0 && bytes.Equal(cur.SHA256, sum[:]) {
			out = cur.Content
			return nil
		}
		now := s.clock.Now()
		newVersion := cur.Version + 1
		if err := r.InsertVersion(ctx, itemID, newVersion, next, sum[:], now); err != nil {
			return err
		}
		if err := r.PruneVersions(ctx, itemID, newVersion-(limit+1)); err != nil {
			return err
		}
		if err := r.TouchItem(ctx, itemID, now); err != nil {
			return err
		}
		out = Content{ItemID: itemID, Version: newVersion, Body: next, UpdatedAt: &now}
		if user, ok := scope.UserID(ctx); ok {
			out.AuthorUserID = &user
		}
		return nil
	})
	if err != nil {
		return Content{}, err
	}
	return out, nil
}

// validateBody applies the body rules: at most 1 MiB of bytes (413), valid
// UTF-8 without NUL (422; Postgres text cannot store NUL).
func validateBody(body string) error {
	if len(body) > MaxBodyBytes {
		return apperr.PayloadTooLarge(MaxBodyBytes)
	}
	if !utf8.ValidString(body) {
		return apperr.Validation(apperr.FieldError{Field: "body", Message: "must be valid UTF-8"})
	}
	if strings.ContainsRune(body, 0) {
		return apperr.Validation(apperr.FieldError{Field: "body", Message: "must not contain NUL characters"})
	}
	return nil
}
