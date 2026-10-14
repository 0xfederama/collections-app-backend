#!/usr/bin/env bash
# AT-17: backup → wipe → restore → verify round trip on an isolated compose project.
# Own project name, own volumes and a random free loopback port, so it cannot touch a dev stack.
# Needs Docker, jq, curl and nc. Run it with `make test-backup`.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/backup-common.sh
. "$here/lib/backup-common.sh" # carrier() and friends; also moves into the repo root

command -v curl >/dev/null 2>&1 || { echo "error: curl is required" >&2; exit 1; }
command -v nc >/dev/null 2>&1 || { echo "error: nc is required" >&2; exit 1; }

# Everything below runs against this project; the scripts read the same variables.
COMPOSE_PROJECT_NAME="collections-at17-$$"
POSTGRES_PASSWORD="$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
API_PORT=0
while :; do
  API_PORT=$((20000 + RANDOM % 20000))
  nc -z 127.0.0.1 "$API_PORT" 2>/dev/null || break
done
export COMPOSE_PROJECT_NAME POSTGRES_PASSWORD API_PORT
BASE="http://127.0.0.1:$API_PORT"

T="$(mktemp -d)"
step() { echo "== $*"; }
teardown() {
  local rc=$?
  if [ "$rc" != 0 ]; then
    echo "--- api logs (last lines) ---" >&2
    docker compose logs --tail 20 api >&2 2>/dev/null || true
  fi
  docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$T"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# call <expected-status> <method> <path> [curl args…]: body in $T/body, headers in $T/hdr.
call() {
  local want="$1" method="$2" path="$3" status
  shift 3
  status="$(curl -sS -o "$T/body" -D "$T/hdr" -w '%{http_code}' -X "$method" "$BASE$path" "$@")"
  [ "$status" = "$want" ] || fail "$method $path answered $status (wanted $want): $(head -c 300 "$T/body")"
}
etag() { grep -i '^etag:' "$T/hdr" | tr -d '\r' | sed 's/^[^:]*: *//'; }
field() { jq -r "$1" "$T/body"; }
json() { call "$@" -H 'Content-Type: application/json'; }

# 8x8 images: two PNGs and a JPEG (generated once with Go's image encoders).
echo 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAFUlEQVR4nGI5ISfHgA0wwRhDQwIwANMiARdqZ5dwAAAAAElFTkSuQmCC' | base64 -d >"$T/red.png"
echo 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAFUlEQVR4nGKRkzvBgA0wwRhDQwIwANHOARewV0+OAAAAAElFTkSuQmCC' | base64 -d >"$T/blue.png"
echo '/9j/2wCEAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDIBCQkJDAsMGA0NGDIhHCEyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMv/AABEIAAgACAMBIgACEQEDEQH/xAGiAAABBQEBAQEBAQAAAAAAAAAAAQIDBAUGBwgJCgsQAAIBAwMCBAMFBQQEAAABfQECAwAEEQUSITFBBhNRYQcicRQygZGhCCNCscEVUtHwJDNicoIJChYXGBkaJSYnKCkqNDU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6g4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2drh4uPk5ebn6Onq8fLz9PX29/j5+gEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoLEQACAQIEBAMEBwUEBAABAncAAQIDEQQFITEGEkFRB2FxEyIygQgUQpGhscEJIzNS8BVictEKFiQ04SXxFxgZGiYnKCkqNTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqCg4SFhoeIiYqSk5SVlpeYmZqio6Slpqeoqaqys7S1tre4ubrCw8TFxsfIycrS09TV1tfY2dri4+Tl5ufo6ery8/T19vf4+fr/2gAMAwEAAhEDEQA/AHUUUV8afAH/2Q==' | base64 -d >"$T/green.jpg"

step "starting an isolated stack ($COMPOSE_PROJECT_NAME, port $API_PORT)"
docker compose up -d --build --wait >/dev/null
curl -fsS "$BASE/readyz" >/dev/null || fail "api is not ready"

# new_item <list-id> <title> <kind>: sets ITEM_ID.
new_item() {
  json 201 POST "/api/v1/lists/$1/items" -d "{\"title\": \"$2\", \"kind\": \"$3\"}"
  ITEM_ID="$(field .id)"
}
# set_image <item-id> <file> <mime>: uploads through the API.
set_image() {
  call 200 GET "/api/v1/items/$1"
  call 200 PUT "/api/v1/items/$1/image" -H "If-Match: $(etag)" -F "file=@$2;type=$3"
}
# save_content <item-id> <body>: one more content version.
save_content() {
  call 200 GET "/api/v1/items/$1/content"
  json 200 PUT "/api/v1/items/$1/content" -H "If-Match: $(etag)" -d "$(jq -n --arg b "$2" '{body: $b}')"
}
# trash <kind-path> <id>: moves a folder/list/item to the trash.
trash() {
  call 200 GET "/api/v1/$1/$2"
  call 204 DELETE "/api/v1/$1/$2" -H "If-Match: $(etag)"
}

step "seeding through the API"
json 201 POST /api/v1/folders -d '{"name": "Media"}'
folder="$(field .id)"
json 201 POST /api/v1/folders -d "{\"name\": \"Sci-Fi\", \"parent_folder_id\": \"$folder\"}"
json 201 POST /api/v1/lists -d "{\"name\": \"Watchlist\", \"folder_id\": \"$folder\"}"
watchlist="$(field .id)"
json 201 POST /api/v1/lists -d '{"name": "Wishlist"}'
wishlist="$(field .id)"
json 201 POST /api/v1/lists -d '{"name": "Old stuff"}'
old_list="$(field .id)"

new_item "$watchlist" "The Matrix" movie
matrix="$ITEM_ID"
new_item "$watchlist" "Blade Runner" movie
runner="$ITEM_ID"
new_item "$wishlist" "Keyboard" generic
keyboard="$ITEM_ID"
new_item "$wishlist" "Thrown away" generic
thrown="$ITEM_ID"
new_item "$old_list" "Inside a trashed list" generic
inside="$ITEM_ID"

set_image "$matrix" "$T/red.png" image/png
set_image "$runner" "$T/green.jpg" image/jpeg
set_image "$keyboard" "$T/blue.png" image/png
set_image "$keyboard" "$T/red.png" image/png # replacement: new attachment, old one removed
set_image "$thrown" "$T/blue.png" image/png
set_image "$inside" "$T/green.jpg" image/jpeg

save_content "$matrix" "# Notes"
save_content "$matrix" $'# Notes\n\nGreat film.'
save_content "$matrix" $'# Notes\n\nGreat film, rewatch in 4K. Ünïcödé ✓'
save_content "$runner" "Final cut is the best one."
save_content "$thrown" "Content of a trashed item."

trash items "$thrown"
trash lists "$old_list"

step "snapshot of the API before the backup"
snapshot() {
  call 200 GET /api/v1/tree && jq -S . "$T/body" >"$1.tree"
  call 200 GET /api/v1/trash && jq -S . "$T/body" >"$1.trash"
  : >"$1.files"
  for id in $(docker compose exec -T db psql -U collection -d collection -At -c 'SELECT id FROM attachments ORDER BY id'); do
    for kind in file thumbnail; do
      echo "$id $kind $(curl -fsS "$BASE/api/v1/attachments/$id/$kind" | shasum -a 256 | cut -d' ' -f1)" >>"$1.files"
    done
  done
  call 200 GET "/api/v1/items/$matrix/content" && jq -S . "$T/body" >"$1.content"
}
snapshot "$T/before"
[ "$(wc -l <"$T/before.files" | tr -d ' ')" = 10 ] || fail "expected 5 attachments (original + thumbnail), got: $(cat "$T/before.files")"

step "backup"
scripts/backup.sh "$T/backups" >"$T/backup.log" || { cat "$T/backup.log" >&2; fail "backup.sh failed"; }
backup="$(find "$T/backups" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
[ -f "$backup/db.dump" ] && [ -f "$backup/manifest.json" ] || fail "backup is missing files"
ls "$backup"/files.tar.* >/dev/null || fail "backup is missing the image archive"
curl -fsS "$BASE/readyz" >/dev/null || fail "api was not restarted after the backup"
jq -e '.row_counts.items == 5 and .row_counts.attachments == 5 and (.content_sha256 | length) == 3 and .migration_version >= 1' "$backup/manifest.json" >/dev/null ||
  fail "unexpected manifest: $(jq -c '{row_counts, migration_version}' "$backup/manifest.json")"
grep -q "$POSTGRES_PASSWORD" "$backup/manifest.json" "$T/backup.log" && fail "the password leaked into the manifest or log"

step "restore refuses a non-empty target and a tampered backup"
if scripts/restore.sh "$backup" >"$T/refuse.log" 2>&1; then fail "restore touched non-empty volumes without --force"; fi
grep -q "not empty" "$T/refuse.log" || fail "refusal message missing: $(cat "$T/refuse.log")"
cp -R "$backup" "$T/tampered"
printf 'x' >>"$T/tampered/db.dump"
if echo restore | scripts/restore.sh "$T/tampered" --force >"$T/tamper.log" 2>&1; then fail "restore accepted a tampered dump"; fi
grep -q "checksum mismatch" "$T/tamper.log" || fail "tamper message missing: $(cat "$T/tamper.log")"
curl -fsS "$BASE/readyz" >/dev/null || fail "a refused restore must leave the stack running"
if echo no | scripts/restore.sh "$backup" --force >"$T/abort.log" 2>&1; then fail "restore ignored a declined prompt"; fi
curl -fsS "$BASE/readyz" >/dev/null || fail "a declined prompt must leave the stack running"

step "wipe all volumes and restore"
docker compose down --volumes --remove-orphans >/dev/null
scripts/restore.sh "$backup" >"$T/restore.log" 2>&1 || { cat "$T/restore.log" >&2; fail "restore.sh failed"; }
scripts/verify-restore.sh "$backup" || fail "verify-restore failed right after a restore"

step "the restored app answers like the original"
snapshot "$T/after"
for part in tree trash files content; do
  diff -u "$T/before.$part" "$T/after.$part" >/dev/null || fail "$part differs after the restore: $(diff "$T/before.$part" "$T/after.$part" | head -5)"
done

step "verify catches corruption"
expect_verify_failure() { # <message fragment>
  if scripts/verify-restore.sh "$backup" >"$T/verify.log" 2>&1; then fail "verify passed despite: $1"; fi
  grep -q "$1" "$T/verify.log" || fail "verify did not report '$1': $(cat "$T/verify.log")"
}
key="$(docker compose exec -T db psql -U collection -d collection -At -c 'SELECT storage_key FROM attachments ORDER BY storage_key LIMIT 1')"
thumb="$(docker compose exec -T db psql -U collection -d collection -At -c 'SELECT thumbnail_storage_key FROM attachments ORDER BY storage_key DESC LIMIT 1')"
carrier rw sh -c "printf x >> '/data/$key'" # flipped original
expect_verify_failure "attachment checksum mismatch: $key"
carrier rw sh -c "rm '/data/$thumb'" # lost thumbnail
expect_verify_failure "attachment thumbnail missing: $thumb"
docker compose exec -T db psql -U collection -d collection -q -c "UPDATE item_content_versions SET body = body || ' tampered' WHERE item_id = '$runner'"
expect_verify_failure "content of item $runner: body differs"
docker compose exec -T db psql -U collection -d collection -q -c "DELETE FROM items WHERE id = '$keyboard'"
expect_verify_failure "row count items"

step "restore --force repairs it"
echo restore | scripts/restore.sh "$backup" --force >"$T/force.log" 2>&1 || { cat "$T/force.log" >&2; fail "restore --force failed"; }
scripts/verify-restore.sh "$backup" || fail "verify failed after restore --force"

echo "AT-17 ok"
