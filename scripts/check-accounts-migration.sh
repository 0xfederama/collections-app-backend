#!/usr/bin/env bash
# F-027 B2: realistic upgrade rehearsal of the accounts migration (000002) plus the ownership backfill.
# It starts a 2.12.0 stack (migration 1 only), seeds a realistic dataset through the OLD API with no
# identity, applies migration 2 and starts THIS checkout's API with OWNER_EMAIL set, then checks that
# no row or file was lost and that every top-level folder and list (trashed ones included) is owned by
# the owner, while another user sees nothing. Own compose project, volumes and loopback port; it
# never touches a dev stack. Needs docker (compose v2), git, jq, curl and nc. Not part of `make check`.
#
# The old image is ghcr.io/0xfederama/collections-app-backend:2.12.0 (override with OLD_IMAGE). When it
# cannot be pulled (the registry is private) the v2.12.0 git tag of this repository is built instead.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib/backup-common.sh
. "$here/lib/backup-common.sh" # dc, psql_db, carrier, counts_json, migration_state; also moves into the repo root

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v nc >/dev/null 2>&1 || die "nc is required"
command -v git >/dev/null 2>&1 || die "git is required"
check_tools

OLD_IMAGE="${OLD_IMAGE:-ghcr.io/0xfederama/collections-app-backend:2.12.0}"
OWNER_EMAIL="owner@migration.test"
OTHER_USER="alice@dev.test"

COMPOSE_PROJECT_NAME="collections-acc-$$"
POSTGRES_PASSWORD="$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')"
API_PORT=0
while :; do
  API_PORT=$((20000 + RANDOM % 20000))
  nc -z 127.0.0.1 "$API_PORT" 2>/dev/null || break
done
export COMPOSE_PROJECT_NAME POSTGRES_PASSWORD API_PORT
BASE="http://127.0.0.1:$API_PORT"

T="$(mktemp -d)"
OLD_REF="$OLD_IMAGE"
BUILT_OLD=""
NEW_REF="collections-acc-new:$$"

step() { echo "== $*"; }
fail() {
  echo "FAIL: $*" >&2
  exit 1
}
teardown() {
  local rc=$?
  if [ "$rc" != 0 ]; then
    echo "--- api logs (last lines) ---" >&2
    dc logs --tail 30 api >&2 2>/dev/null || true
  fi
  dc down --volumes --remove-orphans >/dev/null 2>&1 || true
  docker rmi "$NEW_REF" >/dev/null 2>&1 || true
  if [ -n "$BUILT_OLD" ]; then docker rmi "$BUILT_OLD" >/dev/null 2>&1 || true; fi
  rm -rf "$T"
  exit "$rc"
}
trap teardown EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

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

# --- the old image -------------------------------------------------------------------------------
step "the 2.12.0 image"
if docker pull -q "$OLD_IMAGE" >/dev/null 2>&1; then
  echo "pulled $OLD_IMAGE"
else
  git rev-parse -q --verify refs/tags/v2.12.0 >/dev/null || die "cannot pull $OLD_IMAGE and the git tag v2.12.0 is missing (git fetch --tags)"
  BUILT_OLD="collections-acc-old:$$"
  echo "cannot pull $OLD_IMAGE; building it from the git tag v2.12.0"
  git archive --format=tar v2.12.0 | docker build -q -t "$BUILT_OLD" - >/dev/null
  OLD_REF="$BUILT_OLD"
fi

cat >"$T/old.yaml" <<YAML
services:
  migrate:
    image: $OLD_REF
    pull_policy: never
  api:
    image: $OLD_REF
    pull_policy: never
YAML
cat >"$T/new.yaml" <<YAML
services:
  migrate:
    image: $NEW_REF
  api:
    image: $NEW_REF
YAML
export COMPOSE_FILE="$ROOT/compose.yaml:$T/old.yaml"

step "starting the 2.12.0 stack (project $COMPOSE_PROJECT_NAME, port $API_PORT)"
dc up -d --no-build --wait >/dev/null
curl -fsS "$BASE/readyz" >/dev/null || fail "the old api is not ready"
[ "$(migration_state)" = "1 f" ] || fail "expected schema version 1, got: $(migration_state)"

# --- seed through the old API, with no identity --------------------------------------------------
echo 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAFUlEQVR4nGI5ISfHgA0wwRhDQwIwANMiARdqZ5dwAAAAAElFTkSuQmCC' | base64 -d >"$T/red.png"
echo 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAFUlEQVR4nGKRkzvBgA0wwRhDQwIwANHOARewV0+OAAAAAElFTkSuQmCC' | base64 -d >"$T/blue.png"

folder() { # folder <name> [parent-id]: sets ID
  if [ -n "${2:-}" ]; then json 201 POST /api/v1/folders -d "{\"name\": \"$1\", \"parent_folder_id\": \"$2\"}"; else json 201 POST /api/v1/folders -d "{\"name\": \"$1\"}"; fi
  ID="$(field .id)"
}
list() { # list <name> <type|-> [folder-id]: sets ID
  local type='null' parent='null'
  if [ "$2" != "-" ]; then type="\"$2\""; fi
  if [ -n "${3:-}" ]; then parent="\"$3\""; fi
  json 201 POST /api/v1/lists -d "{\"name\": \"$1\", \"type\": $type, \"folder_id\": $parent}"
  ID="$(field .id)"
}
item() { # item <list-id> <json body>: sets ID
  json 201 POST "/api/v1/lists/$1/items" -d "$2"
  ID="$(field .id)"
}
set_image() { # set_image <item-id> <file>
  call 200 GET "/api/v1/items/$1"
  call 200 PUT "/api/v1/items/$1/image" -H "If-Match: $(etag)" -F "file=@$2;type=image/png"
}
save_content() { # save_content <item-id> <body>
  call 200 GET "/api/v1/items/$1/content"
  json 200 PUT "/api/v1/items/$1/content" -H "If-Match: $(etag)" -d "$(jq -n --arg b "$2" '{body: $b}')"
}
trash() { # trash <folders|lists|items> <id>
  call 200 GET "/api/v1/$1/$2"
  call 204 DELETE "/api/v1/$1/$2" -H "If-Match: $(etag)"
}

step "seeding a realistic dataset through the 2.12.0 API (no identity)"
folder "Media"
media="$ID"
folder "Cinema" "$media"
cinema="$ID"
folder "Classics" "$cinema"
classics="$ID"
folder "Reading"
reading="$ID"
folder "Fiction" "$reading"
fiction="$ID"
folder "Clear out"
clear_out="$ID"

list "Classic films" movie "$classics"
films="$ID"
list "Novels" book "$fiction"
novels="$ID"
list "Gear" wishlist "$media"
gear="$ID"
list "Loose wishlist" wishlist
wishes="$ID"
list "Notes" -
notes="$ID"
list "Old stuff" -
old_list="$ID"
list "To clear" -  "$clear_out"
to_clear="$ID"

first_movie="" first_book="" first_wish="" first_note=""
for n in $(seq 1 20); do
  item "$films" "$(jq -nc --arg t "Film $n" --argjson y $((1930 + n)) '{title: $t, lookup: false, metadata: {release_year: $y, director: "Director \($t)", runtime_minutes: 90}}')"
  if [ -z "$first_movie" ]; then first_movie="$ID"; fi
done
for n in $(seq 1 10); do
  item "$novels" "$(jq -nc --arg t "Novel $n" --argjson p $((100 + n * 10)) '{title: $t, lookup: false, metadata: {author: "Author \($t)", pages: $p}}')"
  if [ -z "$first_book" ]; then first_book="$ID"; fi
done
for n in $(seq 1 12); do
  item "$wishes" "$(jq -nc --arg t "Wish $n" --arg lo "$n.50" --arg hi "$((n + 10)).99" '{title: $t, website: "https://shop.example/\($t)", price: {min: $lo, max: $hi, currency: "EUR"}}')"
  if [ -z "$first_wish" ]; then first_wish="$ID"; fi
done
for n in $(seq 1 4); do
  item "$gear" "$(jq -nc --arg t "Gear $n" --arg a "$((n * 20)).00" '{title: $t, price: {amount: $a, currency: "EUR"}}')"
done
for n in $(seq 1 4); do
  item "$notes" "$(jq -nc --arg t "Note $n" '{title: $t}')"
  if [ -z "$first_note" ]; then first_note="$ID"; fi
done

# Images, and content saved several times (description history).
set_image "$first_movie" "$T/red.png"
set_image "$first_book" "$T/blue.png"
set_image "$first_wish" "$T/red.png"
set_image "$first_wish" "$T/blue.png" # replacement: a new attachment, the old one removed
save_content "$first_movie" "# Notes"
save_content "$first_movie" $'# Notes\n\nGreat film.'
save_content "$first_movie" $'# Notes\n\nGreat film, rewatch in 4K. Ünïcödé ✓'
save_content "$first_book" "Read twice."
save_content "$first_book" $'Read twice.\n\nLend it out.'
save_content "$first_note" "A note with history."

# One lookup record, as a provider import would leave it (no network here).
psql_db -c "UPDATE items SET metadata = metadata || '{\"tmdb_id\":603}'::jsonb WHERE id = '$first_movie';
  INSERT INTO lookup_imports (workspace_id, item_id, provider, source_id, type, imported_fields, imported_title, original_title,
                              fetched_at, refresh_after, expires_at)
  SELECT workspace_id, id, 'tmdb', '603', 'movie', '{\"tmdb_id\":603,\"release_year\":1999}', title, 'Film 1',
         now(), now() + interval '5 months', now() + interval '6 months'
  FROM items WHERE id = '$first_movie'" >/dev/null

# Trash: a folder with children (one list, three items), a list with two items, a single item.
item "$to_clear" '{"title": "Cleared 1"}'
item "$to_clear" '{"title": "Cleared 2"}'
item "$to_clear" '{"title": "Cleared 3"}'
item "$old_list" '{"title": "Old 1"}'
item "$old_list" '{"title": "Old 2"}'
item "$notes" '{"title": "Note thrown away"}'
thrown="$ID"
set_image "$thrown" "$T/red.png"
trash items "$thrown"
trash lists "$old_list"
trash folders "$clear_out"

# --- record the old state ------------------------------------------------------------------------
step "recording the 2.12.0 state"
before="$(counts_json)"
files_before="$(carrier ro find /data -type f </dev/null | wc -l | tr -d ' ')"
exp_folders="$(psql_db -c "SELECT count(*) FROM folders WHERE deleted_at IS NULL")"
exp_lists="$(psql_db -c "SELECT count(*) FROM lists WHERE deleted_at IS NULL")"
exp_batches="$(psql_db -c "SELECT count(*) FROM trash_batches")"
exp_items="$(psql_db -c "SELECT count(*) FROM items WHERE deleted_at IS NULL")"
exp_top="$(psql_db -c "SELECT (SELECT count(*) FROM folders WHERE parent_folder_id IS NULL) + (SELECT count(*) FROM lists WHERE folder_id IS NULL)")"
exp_trashed_top="$(psql_db -c "SELECT (SELECT count(*) FROM folders WHERE parent_folder_id IS NULL AND deleted_at IS NOT NULL) + (SELECT count(*) FROM lists WHERE folder_id IS NULL AND deleted_at IS NOT NULL)")"
echo "tables before: $(jq -c . <<<"$before")"
echo "attachment files before: $files_before; active folders $exp_folders, lists $exp_lists, items $exp_items; trash batches $exp_batches; top-level rows $exp_top ($exp_trashed_top trashed)"
[ "$(jq -r .items <<<"$before")" -ge 50 ] || fail "the seed is smaller than expected: $(jq -c . <<<"$before")"
[ "$exp_trashed_top" -ge 2 ] || fail "expected trashed top-level rows in the seed"

# --- upgrade -------------------------------------------------------------------------------------
step "upgrading: migration 2 and this checkout's API with OWNER_EMAIL set"
dc stop api >/dev/null
export COMPOSE_FILE="$ROOT/compose.yaml:$T/new.yaml"
export OWNER_EMAIL
dc up -d --build --wait >/dev/null
curl -fsS "$BASE/readyz" >/dev/null || fail "the new api is not ready"
[ "$(migration_state)" = "2 f" ] || fail "expected schema version 2, got: $(migration_state)"

# --- checks --------------------------------------------------------------------------------------
step "no row or file was lost"
after="$(counts_json)"
echo "tables after:  $(jq -c . <<<"$after")"
changed="$(jq -nc --argjson b "$before" --argjson a "$after" '[$b | to_entries[] | select($a[.key] != .value) | {table: .key, before: .value, after: $a[.key]}]')"
[ "$changed" = "[]" ] || fail "row counts changed: $changed"
echo "new tables: $(jq -c --argjson b "$before" '[keys[] | select(. as $k | $b | has($k) | not)]' <<<"$after"); users: $(jq -r .users <<<"$after")"
files_after="$(carrier ro find /data -type f </dev/null | wc -l | tr -d ' ')"
[ "$files_before" = "$files_after" ] || fail "attachment files: $files_before before, $files_after after"
[ "$(jq -r .users <<<"$after")" = 1 ] || fail "expected exactly the owner user, got $(jq -r .users <<<"$after")"

step "ownership: every top-level folder and list (trashed too) is the owner's, nested ones have none"
owner_id="$(psql_db -c "SELECT id FROM users WHERE email = '$OWNER_EMAIL'")"
[ -n "$owner_id" ] || fail "the owner user was not created"
q() { psql_db -c "$1"; }
[ "$(q "SELECT count(*) FROM folders WHERE parent_folder_id IS NULL AND owner_user_id IS DISTINCT FROM '$owner_id'")" = 0 ] || fail "a top-level folder is not the owner's"
[ "$(q "SELECT count(*) FROM lists WHERE folder_id IS NULL AND owner_user_id IS DISTINCT FROM '$owner_id'")" = 0 ] || fail "a top-level list is not the owner's"
[ "$(q "SELECT count(*) FROM folders WHERE parent_folder_id IS NOT NULL AND owner_user_id IS NOT NULL")" = 0 ] || fail "a nested folder has an owner"
[ "$(q "SELECT count(*) FROM lists WHERE folder_id IS NOT NULL AND owner_user_id IS NOT NULL")" = 0 ] || fail "a list in a folder has an owner"
owned="$(q "SELECT (SELECT count(*) FROM folders WHERE owner_user_id = '$owner_id') + (SELECT count(*) FROM lists WHERE owner_user_id = '$owner_id')")"
[ "$owned" = "$exp_top" ] || fail "owner has $owned rows, expected $exp_top top-level rows"
echo "owner $OWNER_EMAIL owns $owned top-level rows ($exp_trashed_top of them trashed); everything nested has no owner"

step "the API as the owner returns everything; another dev user sees nothing"
page_total() { jq -r '.page.total' "$T/body"; }
call 200 GET /api/v1/me
[ "$(field .email)" = "$OWNER_EMAIL" ] || fail "the default dev identity is $(field .email), expected $OWNER_EMAIL"
call 200 GET /api/v1/tree
[ "$(jq '.folders | length' "$T/body")" = "$exp_folders" ] || fail "owner tree has $(jq '.folders | length' "$T/body") folders, expected $exp_folders"
[ "$(jq '.lists | length' "$T/body")" = "$exp_lists" ] || fail "owner tree has $(jq '.lists | length' "$T/body") lists, expected $exp_lists"
call 200 GET /api/v1/trash
[ "$(page_total)" = "$exp_batches" ] || fail "owner trash total $(page_total), expected $exp_batches"
call 200 GET "/api/v1/search?q=o&limit=200"
[ "$(page_total)" = "$(q "SELECT count(*) FROM items WHERE deleted_at IS NULL AND title ILIKE '%o%'")" ] || fail "owner search total $(page_total) differs from the database"
call 200 GET "/api/v1/recent-items?limit=50"
[ "$(jq '.data | length' "$T/body")" = 50 ] || fail "owner recent items: $(jq '.data | length' "$T/body")"

call 200 GET /api/v1/me -H "X-Dev-User: $OTHER_USER"
call 200 GET /api/v1/tree -H "X-Dev-User: $OTHER_USER"
[ "$(jq '(.folders | length) + (.lists | length)' "$T/body")" = 0 ] || fail "another user sees the tree: $(head -c 200 "$T/body")"
call 200 GET /api/v1/trash -H "X-Dev-User: $OTHER_USER"
[ "$(page_total)" = 0 ] || fail "another user sees $(page_total) trash batches"
call 200 GET "/api/v1/search?q=o&limit=200" -H "X-Dev-User: $OTHER_USER"
[ "$(page_total)" = 0 ] || fail "another user finds $(page_total) items"
call 404 GET "/api/v1/folders/$media" -H "X-Dev-User: $OTHER_USER"
call 404 GET "/api/v1/lists/$films" -H "X-Dev-User: $OTHER_USER"
call 404 GET "/api/v1/items/$first_movie" -H "X-Dev-User: $OTHER_USER"
call 200 GET "/api/v1/items/$first_movie"

echo
echo "OK: the 2.12.0 data survived migration 2 and the backfill (rows, files, ownership)."
