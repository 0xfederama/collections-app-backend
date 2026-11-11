package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// ErrOwnerEmailRequired stops the startup: top-level rows without an owner exist and nobody is
// configured to take them (OWNER_EMAIL, or the first dev identity with DEV_IDENTITY).
var ErrOwnerEmailRequired = errors.New("top-level folders or lists have no owner yet: set OWNER_EMAIL to the e-mail of the person who owns the existing data")

// BackfillRepo is the work of one backfill transaction.
type BackfillRepo interface {
	// Lock takes the backfill advisory lock until the transaction ends.
	Lock(ctx context.Context) error
	// CountOwnerless counts the top-level folders and lists, in any state, that have no owner.
	CountOwnerless(ctx context.Context) (folders, lists int, err error)
	// UpsertUser returns the user of a lower-cased e-mail, creating it when missing.
	UpsertUser(ctx context.Context, email string) (uuid.UUID, error)
	// Assign sets the owner of exactly the ownerless top-level folders and lists.
	Assign(ctx context.Context, owner uuid.UUID) (folders, lists int, err error)
}

// BackfillStore opens the backfill transaction.
type BackfillStore interface {
	InTx(ctx context.Context, fn func(BackfillRepo) error) error
}

// Backfill gives every ownerless top-level folder and list (any state, trashed included) to the
// user of ownerEmail, creating that user when missing (F-027 decision 15). It names no data: it
// adopts whatever has no owner, so it also covers rows a rolled-back release created. It runs at
// every startup, in one transaction under its own advisory lock, and is a no-op on a database
// with nothing ownerless (a fresh install needs no ownerEmail). With ownerless rows and no
// ownerEmail it returns ErrOwnerEmailRequired and the process must not start.
func Backfill(ctx context.Context, store BackfillStore, ownerEmail string, log *slog.Logger) error {
	email := strings.ToLower(strings.TrimSpace(ownerEmail))
	return store.InTx(ctx, func(r BackfillRepo) error {
		if err := r.Lock(ctx); err != nil {
			return fmt.Errorf("lock backfill: %w", err)
		}
		folders, lists, err := r.CountOwnerless(ctx)
		if err != nil {
			return fmt.Errorf("count ownerless rows: %w", err)
		}
		if folders+lists == 0 {
			return nil
		}
		if email == "" {
			return fmt.Errorf("%w (%d folders, %d lists)", ErrOwnerEmailRequired, folders, lists)
		}
		owner, err := r.UpsertUser(ctx, email)
		if err != nil {
			return fmt.Errorf("owner user: %w", err)
		}
		gotFolders, gotLists, err := r.Assign(ctx, owner)
		if err != nil {
			return fmt.Errorf("assign owner: %w", err)
		}
		log.InfoContext(ctx, "ownership backfill done", "folders", gotFolders, "lists", gotLists)
		return nil
	})
}
