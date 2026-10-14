#!/usr/bin/env bash
# Checks the running database and appdata volume against a backup's manifest.
# Usage: scripts/verify-restore.sh <backup-dir>
# Run it right after a restore: it compares with the backup, so data written since then shows as a difference.
# Exits non-zero with a report on any mismatch.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
if [ "$#" -ne 1 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
  echo "usage: scripts/verify-restore.sh <backup-dir>" >&2
  exit 2
fi
[ -d "$1" ] || { echo "error: not a directory: $1" >&2; exit 1; }
backup="$(cd "$1" && pwd)"

# shellcheck source=lib/backup-common.sh
. "$here/lib/backup-common.sh"
check_tools
db_running || die "the db service is not running"
MANIFEST="$backup/manifest.json"
[ -f "$MANIFEST" ] || die "no manifest.json in $backup"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
problems="$tmp/problems"
: >"$problems"

# 1. Row counts per table.
counts_json >"$tmp/counts.json"
jq -r --slurpfile have "$tmp/counts.json" '
  .row_counts as $want
  | ($want | keys[]) as $t
  | select(($have[0][$t] // null) != $want[$t])
  | "row count \($t): backup has \($want[$t]), database has \($have[0][$t] // "no such table")"' "$MANIFEST" >>"$problems"

# 2. Latest content body of every item.
content_json >"$tmp/content.json"
jq -r --slurpfile have "$tmp/content.json" '
  .content_sha256 as $want
  | (($want | keys) + ($have[0] | keys) | unique[]) as $id
  | select(($have[0][$id] // null) != ($want[$id] // null))
  | "content of item \($id): " + (
      if $want[$id] == null then "not in the backup"
      elif $have[0][$id] == null then "missing in the database"
      else "body differs from the backup" end)' "$MANIFEST" >>"$problems"

# 3. Every attachment: original and thumbnail exist, original's sha256 matches the row.
psql_db -F ' ' -c "SELECT storage_key, thumbnail_storage_key, encode(sha256, 'hex') FROM attachments ORDER BY storage_key" >"$tmp/attachments.txt"
attachments="$(wc -l <"$tmp/attachments.txt" | tr -d ' ')"
# shellcheck disable=SC2016 # runs inside the container
carrier ro sh -c '
  while read -r key thumb want; do
    if [ ! -f "/data/$key" ]; then echo "attachment original missing: $key"; continue; fi
    if [ ! -f "/data/$thumb" ]; then echo "attachment thumbnail missing: $thumb"; fi
    got="$(sha256sum "/data/$key" | cut -d" " -f1)"
    if [ "$got" != "$want" ]; then echo "attachment checksum mismatch: $key"; fi
  done' <"$tmp/attachments.txt" >>"$problems"

if [ -s "$problems" ]; then
  echo "VERIFY FAILED: $(wc -l <"$problems" | tr -d ' ') problem(s) against $backup" >&2
  sed 's/^/  - /' "$problems" >&2
  exit 1
fi
tables="$(jq '.row_counts | length' "$MANIFEST")"
items="$(jq '.content_sha256 | length' "$MANIFEST")"
echo "verify ok: $tables tables, $items content bodies, $attachments attachments match $backup"
