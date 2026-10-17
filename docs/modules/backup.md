# backup

## Purpose

Backs up Postgres and the uploaded images (`appdata` volume) together as one checksummed directory, then restores it and verifies the result against its manifest. Host-side shell scripts that need Docker Compose v2 and `jq`. Design context: `/Users/federico/Developer/collections-app/design-docs/` (BACKUP_AND_DEPLOYMENT §4, D-066).

## Key files

- `scripts/backup.sh`: stops `api`, `pg_dump -Fc`, archives `images/`, writes manifest
- `scripts/restore.sh`: checks hashes, empties volumes, restores, verifies, migrates, starts `api`
- `scripts/verify-restore.sh`: compares row counts, content hashes and attachment files with the manifest
- `scripts/lib/backup-common.sh`: shared helpers (`carrier`, `appdata_volume`, `counts_json`, `check_backup_dir`)
- `scripts/test-backup.sh`: AT-17 round trip on an isolated compose project
- `Makefile`: `lint-scripts` (shellcheck, part of `make check`), `test-backup`

## Interfaces

- `scripts/backup.sh [dest]`: writes `<dest>/<UTC ts>/` with `db.dump`, `files.tar.zst` or `files.tar.gz`, `manifest.json`; default dest is `backups/` in the repo root
- `scripts/restore.sh <backup-dir> [--force]`: `--force` deletes non-empty volumes after a typed `restore`
- `scripts/verify-restore.sh <backup-dir>`: exits 1 and lists each difference
- `manifest.json` (`format: 1`): `migration_version`, `app_version`, `files.{db,images}.sha256`, `row_counts`, `content_sha256` (item id → body hash)
- `make test-backup`: AT-17 (`scripts/test-backup.sh`), about a minute; needs Docker, jq, curl, nc
- `make lint-scripts`: shellcheck over `scripts/` through the pinned `koalaman/shellcheck` image

## Gotchas

- `backup.sh` stops `api` only if it was running; a trap restarts it on any exit. A failed run removes its `.partial` directory.
- `backup.sh` refuses a dirty `schema_migrations`. `restore.sh` refuses a backup whose `migration_version` is newer than the checkout.
- Restore runs verify before `migrate up`. Verify compares with the backup, so run it right after a restore; later edits show as differences.
- `--force` runs `docker compose down --volumes --remove-orphans`. The prompt reads stdin, so `echo restore | scripts/restore.sh … --force` works.
- Never use a bare `-v appdata:/data`: that is a global volume. Use `appdata_volume` and `carrier`. Containers that must not read stdin get `</dev/null`.
- Wait for the final server over TCP (`pg_isready -h 127.0.0.1`); the socket answers during the init phase.
- Without `zstd` on the host, `gzip` is used. The manifest records the archive name and hash.
- Restored files are chowned to 65532 (the api's nonroot user).
- `POSTGRES_PASSWORD`, `API_PORT`, `COMPOSE_PROJECT_NAME` come from the environment, else `.env`.
- Checksums detect corruption, not a forged archive: restore only backups you made. Backups are unencrypted; `backups/` is gitignored.
- Backups contain imported TMDB data. Retire each backup within six months under [TMDB API terms](https://www.themoviedb.org/api-terms-of-use); production backup retention is configured separately.
