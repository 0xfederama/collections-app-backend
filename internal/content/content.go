// Package content owns the Markdown content of items: the current body, its
// saved versions, restore and retention (API.md §5 Content, DATA_MODEL §2
// item_content_versions, D-052). It knows nothing about HTTP: it returns typed
// errors from internal/apperr.
//
// The current version of an item is the highest row of item_content_versions
// (0 when it was never saved). Content versions are an ETag stream of their own
// ("c-N"): a save bumps items.updated_at but never items.version.
package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/0xfederama/collections-app-backend/internal/apperr"
	"github.com/0xfederama/collections-app-backend/internal/platform/clock"
	"github.com/0xfederama/collections-app-backend/internal/platform/database"
	"github.com/0xfederama/collections-app-backend/internal/platform/database/sqlc"
)

// MaxBodyBytes is the largest Markdown body: 1 MiB of UTF-8 bytes (API.md §6).
const MaxBodyBytes = 1 << 20

// DB is satisfied by *pgxpool.Pool: plain queries plus transactions.
type DB interface {
	database.TxBeginner
	sqlc.DBTX
}

// Service is the content service of one workspace.
type Service struct {
	db          DB
	clock       clock.Clock
	workspaceID uuid.UUID
}

// New returns a Service bound to the single workspace.
func New(db DB, clk clock.Clock, workspaceID uuid.UUID) *Service {
	return &Service{db: db, clock: clk, workspaceID: workspaceID}
}

// Content is one version of an item's Markdown. Version 0 is the empty,
// never-saved content: Body is "" and UpdatedAt is nil.
type Content struct {
	ItemID    uuid.UUID
	Version   int
	Body      string
	UpdatedAt *time.Time
}

// VersionInfo describes one saved version without its body.
type VersionInfo struct {
	Version   int
	SizeBytes int
	CreatedAt time.Time
}

// Get returns the current content of an active item.
func (s *Service) Get(ctx context.Context, itemID uuid.UUID) (Content, error) {
	q := sqlc.New(s.db)
	if _, err := q.ItemIsActive(ctx, sqlc.ItemIsActiveParams{ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
		return Content{}, notFoundOr(err, "item")
	}
	return latest(ctx, q, itemID)
}

// Version returns one saved version (404 if it never existed or was pruned).
func (s *Service) Version(ctx context.Context, itemID uuid.UUID, version int) (Content, error) {
	q := sqlc.New(s.db)
	if _, err := q.ItemIsActive(ctx, sqlc.ItemIsActiveParams{ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
		return Content{}, notFoundOr(err, "item")
	}
	row, err := q.GetContentVersion(ctx, sqlc.GetContentVersionParams{ItemID: itemID, Version: int32(version)})
	if err != nil {
		return Content{}, notFoundOr(err, "content version")
	}
	return fromRow(row.ItemID, row.Version, row.Body, row.CreatedAt), nil
}

// Versions lists the saved versions, newest first.
func (s *Service) Versions(ctx context.Context, itemID uuid.UUID) ([]VersionInfo, error) {
	q := sqlc.New(s.db)
	if _, err := q.ItemIsActive(ctx, sqlc.ItemIsActiveParams{ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
		return nil, notFoundOr(err, "item")
	}
	rows, err := q.ListContentVersions(ctx, itemID)
	if err != nil {
		return nil, fmt.Errorf("list content versions: %w", err)
	}
	out := make([]VersionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, VersionInfo{Version: int(r.Version), SizeBytes: int(r.SizeBytes), CreatedAt: r.CreatedAt})
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
	return s.write(ctx, itemID, ifMatch, func(*sqlc.Queries) (string, error) { return body, nil })
}

// Restore saves the body of an older version as a new version if ifMatch is
// the current version. A pruned or unknown version is a NotFound.
func (s *Service) Restore(ctx context.Context, itemID uuid.UUID, ifMatch, version int) (Content, error) {
	return s.write(ctx, itemID, ifMatch, func(q *sqlc.Queries) (string, error) {
		row, err := q.GetContentVersion(ctx, sqlc.GetContentVersionParams{ItemID: itemID, Version: int32(version)})
		if err != nil {
			return "", notFoundOr(err, "content version")
		}
		return row.Body, nil
	})
}

// write is the one save path: lock the item, compare the version, insert the
// next version unless the body is unchanged, prune, touch updated_at. body
// runs inside the transaction (restore reads the old version there).
func (s *Service) write(ctx context.Context, itemID uuid.UUID, ifMatch int, body func(*sqlc.Queries) (string, error)) (Content, error) {
	var out Content
	err := database.InTx(ctx, s.db, func(q *sqlc.Queries) error {
		if _, err := q.LockItemForContent(ctx, sqlc.LockItemForContentParams{ID: itemID, WorkspaceID: s.workspaceID}); err != nil {
			return notFoundOr(err, "item")
		}
		cur, err := latestRow(ctx, q, itemID)
		if err != nil {
			return err
		}
		if cur.version != ifMatch {
			return apperr.VersionConflict(cur.version)
		}
		next, err := body(q)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(next))
		if cur.version == 0 && next == "" {
			out = Content{ItemID: itemID} // version 0 already is the empty content
			return nil
		}
		if cur.version > 0 && bytes.Equal(cur.sha256, sum[:]) {
			out = Content{ItemID: itemID, Version: cur.version, Body: cur.body, UpdatedAt: &cur.createdAt}
			return nil
		}
		now := s.clock.Now()
		newVersion := cur.version + 1
		if err := q.InsertContentVersion(ctx, sqlc.InsertContentVersionParams{
			ItemID: itemID, Version: int32(newVersion), Body: next, SizeBytes: int32(len(next)), Sha256: sum[:], CreatedAt: now,
		}); err != nil {
			return fmt.Errorf("insert content version: %w", err)
		}
		limit, err := q.GetMarkdownHistoryLimit(ctx, s.workspaceID)
		if err != nil {
			return fmt.Errorf("get markdown history limit: %w", err)
		}
		if err := q.PruneContentVersions(ctx, sqlc.PruneContentVersionsParams{ItemID: itemID, UpTo: int32(newVersion) - (limit + 1)}); err != nil {
			return fmt.Errorf("prune content versions: %w", err)
		}
		if err := q.TouchItemUpdatedAt(ctx, sqlc.TouchItemUpdatedAtParams{ID: itemID, WorkspaceID: s.workspaceID, Now: now}); err != nil {
			return fmt.Errorf("touch item: %w", err)
		}
		out = Content{ItemID: itemID, Version: newVersion, Body: next, UpdatedAt: &now}
		return nil
	})
	return out, err
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

type latestVersion struct {
	version   int
	body      string
	sha256    []byte
	createdAt time.Time
}

// latestRow reads the highest version; none means version 0.
func latestRow(ctx context.Context, q *sqlc.Queries, itemID uuid.UUID) (latestVersion, error) {
	row, err := q.GetLatestContentVersion(ctx, itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return latestVersion{}, nil
	}
	if err != nil {
		return latestVersion{}, fmt.Errorf("get latest content version: %w", err)
	}
	return latestVersion{version: int(row.Version), body: row.Body, sha256: row.Sha256, createdAt: row.CreatedAt}, nil
}

func latest(ctx context.Context, q *sqlc.Queries, itemID uuid.UUID) (Content, error) {
	cur, err := latestRow(ctx, q, itemID)
	if err != nil {
		return Content{}, err
	}
	if cur.version == 0 {
		return Content{ItemID: itemID}, nil
	}
	return Content{ItemID: itemID, Version: cur.version, Body: cur.body, UpdatedAt: &cur.createdAt}, nil
}

func fromRow(itemID uuid.UUID, version int32, body string, createdAt time.Time) Content {
	return Content{ItemID: itemID, Version: int(version), Body: body, UpdatedAt: &createdAt}
}

func notFoundOr(err error, resource string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(resource)
	}
	return fmt.Errorf("%s: %w", resource, err)
}
