#!/usr/bin/env bash
# Restores a backup made by backup.sh into fresh volumes (BACKUP_AND_DEPLOYMENT §4).
# Usage: scripts/restore.sh <backup-dir> [--force]
# Refuses to touch non-empty pgdata/appdata volumes. --force deletes them after a typed confirmation.
# Order: check hashes → empty volumes → restore dump → extract images → verify → migrate up → start api.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
force=0
backup_arg=""
for arg in "$@"; do
  case "$arg" in
    --force) force=1 ;;
    -*) backup_arg="" && break ;;
    *) if [ -z "$backup_arg" ]; then backup_arg="$arg"; else backup_arg="" && break; fi ;;
  esac
done
if [ -z "$backup_arg" ] || [ ! -d "$backup_arg" ]; then
  echo "usage: scripts/restore.sh <backup-dir> [--force]" >&2
  exit 2
fi
backup="$(cd "$backup_arg" && pwd)"

# shellcheck source=lib/backup-common.sh
. "$here/lib/backup-common.sh"
check_tools
check_backup_dir "$backup"

manifest_migration="$(jq -r '.migration_version' "$MANIFEST")"
local_migration="$(latest_local_migration)"
if [ "$manifest_migration" -gt "$local_migration" ]; then
  die "the backup is at migration $manifest_migration but this checkout only has up to $local_migration: update the code first"
fi

# Which target volumes hold data? (mounting creates them empty if they do not exist yet)
nonempty=()
if [ -n "$(dc run --rm --no-deps -T --entrypoint sh db -c 'ls -A /var/lib/postgresql' 2>/dev/null </dev/null)" ]; then
  nonempty+=(pgdata)
fi
if [ -n "$(carrier ro sh -c 'ls -A /data' 2>/dev/null </dev/null)" ]; then
  nonempty+=(appdata)
fi

if [ "${#nonempty[@]}" -gt 0 ]; then
  if [ "$force" != 1 ]; then
    die "target volume(s) not empty: ${nonempty[*]}. Re-run with --force to delete them first"
  fi
  answer=""
  read -r -p "This DELETES the current database and uploaded files (${nonempty[*]}). Type 'restore' to continue: " answer || true
  if [ "$answer" != "restore" ]; then
    echo "aborted"
    exit 1
  fi
  echo "removing containers and volumes"
  dc down --volumes --remove-orphans
fi

echo "starting a fresh database"
dc up -d --no-deps --wait db >/dev/null
wait_for_db

echo "restoring the database"
dc exec -T db pg_restore -U collection -d collection --no-owner --no-privileges --single-transaction --exit-on-error <"$DB_DUMP"

echo "extracting images"
case "$FILES_ARCHIVE" in
  *.zst) need zstd "needed to read $FILES_ARCHIVE"; decompress=(zstd -dc -q) ;;
  *.gz) decompress=(gzip -dc) ;;
  *) die "unknown archive type: $FILES_ARCHIVE" ;;
esac
# The files belong to the api's unprivileged user, not root.
"${decompress[@]}" "$FILES_ARCHIVE" |
  carrier rw sh -c "tar -xf - -C /data && chown -R $API_UID:$API_UID /data"

echo "verifying against the manifest"
"$here/verify-restore.sh" "$backup"

echo "applying newer migrations (if any)"
dc run --rm migrate

echo "starting api"
dc up -d --wait api
echo "restore complete from $backup"
