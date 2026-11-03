// Package db embeds the SQL migrations so the API binary can apply them itself
// (`/app/api migrate`) without a migrations directory next to it.
package db

import "embed"

// Migrations holds db/migrations/*.sql at the root of the FS.
//
//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the embedded migration files, rooted at the directory.
func Migrations() embed.FS { return migrations }
