#!/usr/bin/env bash
# Backs up Postgres and the uploaded images together (BACKUP_AND_DEPLOYMENT §4).
# Usage: scripts/backup.sh [dest]    (default: ./backups in the repo root)
# Stops the api while it runs (the trap restarts it on any exit), then writes
# <dest>/<UTC timestamp>/{db.dump, files.tar.zst, manifest.json}.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
if [ "$#" -gt 1 ] || [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  echo "usage: scripts/backup.sh [dest]" >&2
  exit 2
fi
if [ -n "${1:-}" ]; then
  mkdir -p "$1"
  dest="$(cd "$1" && pwd)"
else
  mkdir -p "$here/../backups"
  dest="$(cd "$here/../backups" && pwd)"
fi

# shellcheck source=lib/backup-common.sh
. "$here/lib/backup-common.sh"
check_tools

db_running || die "the db service is not running (docker compose up -d db)"

ts="$(date -u +%Y%m%dT%H%M%SZ)"
final="$dest/$ts"
work="$dest/$ts.partial"
[ ! -e "$final" ] || die "$final already exists"

restart_api=0
cleanup() {
  local rc=$?
  if [ "$restart_api" = 1 ]; then
    echo "restarting api"
    dc start api >/dev/null || echo "warning: could not restart api, run: docker compose start api" >&2
  fi
  if [ -d "$work" ]; then
    rm -rf "$work"
    echo "backup failed, partial output removed" >&2
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

umask 077
mkdir "$work"

if api_running; then
  restart_api=1
  echo "stopping api (writes paused)"
  dc stop api >/dev/null
fi

read -r migration dirty < <(migration_state) || die "could not read schema_migrations"
[ "$dirty" = "f" ] || die "schema_migrations is dirty at version $migration, fix the migration first"

echo "dumping database"
dc exec -T db pg_dump -U collection -d collection -Fc >"$work/db.dump"
[ -s "$work/db.dump" ] || die "pg_dump produced an empty file"

# Compression is chosen on the host (the db image has no zstd); gzip is the fallback.
if command -v zstd >/dev/null 2>&1; then
  files_name=files.tar.zst
  compress=(zstd -q -T0 -c)
else
  echo "zstd not found on the host, falling back to gzip (files.tar.gz)" >&2
  files_name=files.tar.gz
  compress=(gzip -c)
fi
echo "archiving images ($files_name)"
# `-T /dev/null` writes an empty archive when nothing was uploaded yet.
carrier ro sh -c 'cd /data && if [ -d images ]; then tar -cf - images; else tar -cf - -T /dev/null; fi' |
  "${compress[@]}" >"$work/$files_name"
[ -s "$work/$files_name" ] || die "image archive is empty"

echo "writing manifest"
counts="$(counts_json)"
contents="$(content_json)"
app_version="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo unknown)"
jq -n \
  --argjson format "$MANIFEST_FORMAT" \
  --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson migration "$migration" \
  --arg app_version "$app_version" \
  --arg db_sha "$(sha256_file "$work/db.dump")" \
  --arg files_name "$files_name" \
  --arg files_sha "$(sha256_file "$work/$files_name")" \
  --argjson counts "$counts" \
  --argjson contents "$contents" \
  '{format: $format, created_at: $created_at, migration_version: $migration, app_version: $app_version,
    files: {db: {name: "db.dump", sha256: $db_sha}, images: {name: $files_name, sha256: $files_sha}},
    row_counts: $counts, content_sha256: $contents}' >"$work/manifest.json"

mv "$work" "$final"
echo "backup written to $final"
echo "  keep copies off this machine; backups hold your data, never commit them"
