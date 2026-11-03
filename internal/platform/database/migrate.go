package database

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// ErrDirty means schema_migrations is marked dirty: an earlier run stopped
// half way and the schema needs a human before anything else is applied.
var ErrDirty = errors.New("schema_migrations is dirty")

// Migrate applies every pending migration in dir of fsys to databaseURL (a
// postgres:// URL) and returns the version reached; no pending migration is
// not an error. It keeps golang-migrate's schema_migrations table, so a
// database migrated earlier by the migrate CLI continues where it stopped.
//
// Each file runs in one transaction (D-118): the pgx5 driver sends a file as
// one multi-statement simple query, which Postgres commits or rolls back as a
// whole, and migrations_test rejects statements that cannot run that way.
// So when a file fails the schema is unchanged, and Migrate clears the dirty
// flag golang-migrate set before running it. A database already dirty on
// entry was not left by this code: Migrate refuses it.
func Migrate(fsys fs.FS, dir, databaseURL string) (uint, error) {
	src, err := iofs.New(fsys, dir)
	if err != nil {
		return 0, fmt.Errorf("open embedded migrations: %w", err)
	}
	dsn := strings.Replace(databaseURL, "postgres://", "pgx5://", 1)
	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return 0, fmt.Errorf("connect for migrations: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	for {
		prev, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			prev, dirty, err = 0, false, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read migration version: %w", err)
		}
		if dirty {
			return prev, fmt.Errorf("%w at version %d: inspect the schema by hand, then mark the right version with `migrate force`; nothing was applied", ErrDirty, prev)
		}
		err = m.Steps(1)
		if errors.Is(err, os.ErrNotExist) {
			return prev, nil // no next file: up to date
		}
		if err != nil {
			// The file's transaction rolled back, so the schema is still at prev.
			if ferr := m.Force(forceVersion(prev)); ferr != nil {
				return prev, fmt.Errorf("migrate up after %d: %w (clearing the dirty flag also failed: %w)", prev, err, ferr)
			}
			return prev, fmt.Errorf("migrate up after %d: %w (rolled back, schema unchanged at %d)", prev, err, prev)
		}
	}
}

// forceVersion maps "no migration applied" to golang-migrate's NilVersion.
func forceVersion(v uint) int {
	if v == 0 {
		return -1 // golang-migrate's NilVersion
	}
	return int(v)
}
