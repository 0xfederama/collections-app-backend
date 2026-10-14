# shellcheck shell=bash
# Shared helpers for backup.sh, restore.sh and verify-restore.sh (BACKUP_AND_DEPLOYMENT §4).
# Source it after `set -euo pipefail`. It moves into the repo root, so resolve relative
# user paths BEFORE sourcing. Honors COMPOSE_PROJECT_NAME, API_PORT and POSTGRES_PASSWORD
# from the environment (the AT-17 test relies on that); otherwise reads .env like compose.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT" || exit 1

# The environment wins over .env, as it does for docker compose itself.
if [ -f .env ]; then
  while IFS='=' read -r key value; do
    case "$key" in
      POSTGRES_PASSWORD | API_PORT | APP_ENV | COMPOSE_PROJECT_NAME)
        if [ -z "${!key:-}" ]; then export "$key=$value"; fi
        ;;
    esac
  done < <(grep -E '^(POSTGRES_PASSWORD|API_PORT|APP_ENV|COMPOSE_PROJECT_NAME)=' .env || true)
fi

# shellcheck disable=SC2034 # used by restore.sh
# UID of the distroless "nonroot" user in the api image (Dockerfile).
API_UID=65532
MANIFEST_FORMAT=1

die() {
  echo "error: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1 ($2)"
}

check_tools() {
  need docker "Docker with Compose v2"
  docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"
  need jq "install it, e.g. 'brew install jq' or 'apt install jq'"
}

dc() {
  docker compose "$@"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

db_running() {
  dc ps --status running --services 2>/dev/null | grep -qx db
}

api_running() {
  dc ps --status running --services 2>/dev/null | grep -qx api
}

# psql against the db container: unaligned, tuples only, stops on the first error.
# The local socket inside the container needs no password.
psql_db() {
  dc exec -T db psql -U collection -d collection -X -q -v ON_ERROR_STOP=1 -At "$@" </dev/null
}

# Name of the project's appdata volume, created with compose's labels if it does not exist yet
# (a bare `-v appdata:/data` would mean a different, global volume called "appdata").
appdata_volume() {
  local name
  name="$(dc config --format json | jq -r '.volumes.appdata.name')"
  [ -n "$name" ] && [ "$name" != null ] || die "could not resolve the appdata volume name"
  if ! docker volume inspect "$name" >/dev/null 2>&1; then
    docker volume create \
      --label "com.docker.compose.project=$(dc config --format json | jq -r .name)" \
      --label com.docker.compose.volume=appdata "$name" >/dev/null
  fi
  echo "$name"
}

# One-off container on the db image with the appdata volume at /data, for tar/sha256sum
# (the api image is distroless and has no shell). Usage: carrier <ro|rw> <command> [args…]
# Extra stdin is passed through. pgdata is mounted too (its service default) but never touched.
carrier() {
  local mode="$1" vol
  shift
  vol="$(appdata_volume)"
  dc run --rm --no-deps -T -v "$vol:/data:$mode" --entrypoint "$1" db "${@:2}"
}

# Wait for the *final* server: the image's init phase runs a temporary server that has no TCP
# listener, so check over TCP rather than the socket.
wait_for_db() {
  for _ in $(seq 1 90); do
    if dc exec -T db pg_isready -h 127.0.0.1 -U collection -d collection -q 2>/dev/null </dev/null; then
      return 0
    fi
    sleep 1
  done
  die "database did not become ready in 90s (docker compose logs db)"
}

# JSON object {"table": rowcount, …} for every table in the public schema (including schema_migrations).
counts_json() {
  psql_db -c "
    SELECT coalesce(jsonb_object_agg(tablename,
      (xpath('/row/c/text()', query_to_xml(format('select count(*) as c from %I.%I', schemaname, tablename), false, true, '')))[1]::text::bigint
    ), '{}'::jsonb)::text
    FROM pg_tables WHERE schemaname = 'public'"
}

# JSON object {"item_id": "sha256 hex of the latest content body", …}.
content_json() {
  psql_db -c "
    SELECT coalesce(jsonb_object_agg(item_id, h), '{}'::jsonb)::text
    FROM (SELECT DISTINCT ON (item_id) item_id, encode(sha256(convert_to(body, 'UTF8')), 'hex') AS h
          FROM item_content_versions ORDER BY item_id, version DESC) s"
}

# "version dirty" of golang-migrate's bookkeeping table.
migration_state() {
  psql_db -F ' ' -c "SELECT version, dirty FROM schema_migrations"
}

# Highest migration number shipped with this checkout.
latest_local_migration() {
  local f max=0 n
  for f in db/migrations/*.up.sql; do
    [ -e "$f" ] || continue
    n="$(basename "$f")"
    n=$((10#${n%%_*}))
    if [ "$n" -gt "$max" ]; then max="$n"; fi
  done
  echo "$max"
}

# Verifies format, hashes and file presence of a backup directory. Sets MANIFEST to its path.
check_backup_dir() {
  local dir="$1" db_name files_name want got
  [ -d "$dir" ] || die "not a directory: $dir"
  MANIFEST="$dir/manifest.json"
  [ -f "$MANIFEST" ] || die "no manifest.json in $dir"
  jq -e --argjson f "$MANIFEST_FORMAT" '.format == $f' "$MANIFEST" >/dev/null ||
    die "unsupported manifest format (expected $MANIFEST_FORMAT)"
  db_name="$(jq -r '.files.db.name' "$MANIFEST")"
  files_name="$(jq -r '.files.images.name' "$MANIFEST")"
  # Names come from the manifest: only plain file names inside the backup directory.
  case "$db_name$files_name" in */* | *..*) die "manifest names must be plain file names" ;; esac
  for name in "$db_name" "$files_name"; do
    [ -f "$dir/$name" ] || die "missing $name in $dir"
  done
  want="$(jq -r '.files.db.sha256' "$MANIFEST")"
  got="$(sha256_file "$dir/$db_name")"
  [ "$want" = "$got" ] || die "checksum mismatch for $db_name (backup is corrupt)"
  want="$(jq -r '.files.images.sha256' "$MANIFEST")"
  got="$(sha256_file "$dir/$files_name")"
  [ "$want" = "$got" ] || die "checksum mismatch for $files_name (backup is corrupt)"
  # shellcheck disable=SC2034 # read by restore.sh
  DB_DUMP="$dir/$db_name"
  # shellcheck disable=SC2034
  FILES_ARCHIVE="$dir/$files_name"
}
