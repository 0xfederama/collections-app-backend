package database

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Migrate applies every pending migration in dir of fsys to databaseURL (a
// postgres:// URL). It keeps golang-migrate's schema_migrations table, so a
// database migrated earlier by the migrate CLI continues where it stopped.
// It returns the version reached; no pending migration is not an error.
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
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("migrate up: %w", err)
	}
	v, dirty, err := m.Version()
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return v, fmt.Errorf("schema_migrations is dirty at version %d", v)
	}
	return v, nil
}
