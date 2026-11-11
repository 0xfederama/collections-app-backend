#!/usr/bin/env bash
# F003-B1: API smoke test over HTTP against a running stack (curl + jq only).
# Defaults to the ./deploy/dev stack (fresh). Never run it against ./deploy/test: that is the user's manual environment. Run it with `make smoke`.
# It creates its own uniquely named data and removes it on exit, whatever the outcome.
set -euo pipefail

API_URL="${API_URL:-http://localhost:8090}"
API_URL="${API_URL%/}"
API="$API_URL/api/v1"
RUN="smoke-$$-$(date +%s)"

# F-027: the API needs an identity. A dev/test stack (DEV_IDENTITY=true) answers a request without
# one as its first dev identity, so nothing is sent by default; SMOKE_DEV_USER=<email> sends it as
# X-Dev-User. The 2.12.0 API ignores the header. Every curl below goes through this wrapper.
curl() {
  if [ -n "${SMOKE_DEV_USER:-}" ]; then
    command curl -H "X-Dev-User: $SMOKE_DEV_USER" "$@"
  else
    command curl "$@"
  fi
}

for tool in curl jq base64; do
  command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required" >&2; exit 1; }
done

T="$(mktemp -d)"
FOLDER_ID=""
STEP="start"

step() {
  STEP="$*"
  echo "== $STEP"
}
fail() { # fail <expected> <got>
  echo "smoke FAILED at step: $STEP (expected $1, got $2)" >&2
  exit 1
}

# Best effort: trash the folder if it is still active, then purge every trash entry of this run.
cleanup() {
  local rc=$?
  set +e
  if [ -n "$FOLDER_ID" ]; then
    local status etag batch
    status="$(curl -sS -o "$T/c_body" -D "$T/c_hdr" -w '%{http_code}' "$API/folders/$FOLDER_ID" 2>/dev/null)"
    if [ "$status" = 200 ]; then
      etag="$(grep -i '^etag:' "$T/c_hdr" | tr -d '\r' | sed 's/^[^:]*: *//')"
      curl -sS -o /dev/null -X DELETE -H "If-Match: $etag" "$API/folders/$FOLDER_ID" 2>/dev/null
    fi
    batch="$(curl -sS "$API/trash?limit=200" 2>/dev/null | jq -r --arg id "$FOLDER_ID" '.data[]? | select(.root_id == $id) | .id' 2>/dev/null)"
    for b in $batch; do
      curl -sS -o /dev/null -X DELETE "$API/trash/$b" 2>/dev/null
    done
  fi
  rm -rf "$T"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# call <expected-status> <method> <path> [curl args…]: body in $T/body, headers in $T/hdr.
call() {
  local want="$1" method="$2" path="$3" status
  shift 3
  status="$(curl -sS -o "$T/body" -D "$T/hdr" -w '%{http_code}' -X "$method" "$API_URL$path" "$@")" ||
    fail "$want" "no response from $API_URL (curl failed)"
  [ "$status" = "$want" ] || fail "HTTP $want" "HTTP $status: $(head -c 300 "$T/body")"
}
json() { call "$@" -H 'Content-Type: application/json'; }
etag() { grep -i '^etag:' "$T/hdr" | tr -d '\r' | sed 's/^[^:]*: *//'; }
field() { jq -r "$1" "$T/body"; }
expect() { # expect <what> <expected> <got>
  [ "$2" = "$3" ] || { STEP="$STEP: $1"; fail "$2" "$3"; }
}
trash_batch() { # trash_batch <root id>: batch id of the trash entry of that root
  call 200 GET '/api/v1/trash?limit=200'
  jq -r --arg id "$1" '[.data[] | select(.root_id == $id)][0].id // empty' "$T/body"
}

# 8x8 PNG.
echo 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAFUlEQVR4nGI5ISfHgA0wwRhDQwIwANMiARdqZ5dwAAAAAElFTkSuQmCC' | base64 -d >"$T/img.png"

step "GET /readyz"
call 200 GET /readyz

step "create folder"
json 201 POST /api/v1/folders -d "{\"name\":\"$RUN folder\"}"
FOLDER_ID="$(field .id)"
FOLDER_ETAG="$(etag)"

step "create vehicle list"
json 201 POST /api/v1/lists -d "{\"name\":\"$RUN vehicles\",\"type\":\"vehicle\",\"folder_id\":\"$FOLDER_ID\"}"
LIST_ID="$(field .id)"
expect "list type" vehicle "$(field .type)"

step "create item with vehicle metadata"
json 201 POST "/api/v1/lists/$LIST_ID/items" \
  -d "{\"title\":\"$RUN big\",\"metadata\":{\"model_year\":2020,\"displacement_cc\":900}}"
ITEM_ID="$(field .id)"
ITEM_ETAG="$(etag)"
expect "item type" vehicle "$(field .type)"
expect "displacement_cc" 900 "$(field .metadata.displacement_cc)"

step "create wishlist item with a single price"
json 201 POST /api/v1/lists -d "{\"name\":\"$RUN wishes\",\"type\":\"wishlist\",\"folder_id\":\"$FOLDER_ID\"}"
WISH_LIST_ID="$(field .id)"
json 201 POST "/api/v1/lists/$WISH_LIST_ID/items" \
  -d "{\"title\":\"$RUN gift\",\"price\":{\"amount\":\"1299.00\",\"currency\":\"EUR\"}}"
expect "price min" "1299.00" "$(field .price.min)"
expect "price max" "1299.00" "$(field .price.max)"

step "create second item and sort by displacement_cc"
json 201 POST "/api/v1/lists/$LIST_ID/items" -d "{\"title\":\"$RUN small\",\"metadata\":{\"displacement_cc\":125}}"
SMALL_ID="$(field .id)"
call 200 GET "/api/v1/lists/$LIST_ID/items?sort=displacement_cc"
expect "order by displacement_cc (small first)" "$SMALL_ID $ITEM_ID" "$(jq -r '[.data[].id] | join(" ")' "$T/body")"

step "upload image to the item"
call 200 PUT "/api/v1/items/$ITEM_ID/image" -H "If-Match: $ITEM_ETAG" -F "file=@$T/img.png;type=image/png"
expect "image source" upload "$(field .image.source)"
THUMB_URL="$(field .image.thumbnail_url)"
ITEM_ETAG="$(etag)"

step "GET attachment thumbnail"
call 200 GET "$THUMB_URL"
ctype="$(grep -i '^content-type:' "$T/hdr" | tr -d '\r' | sed 's/^[^:]*: *//')"
case "$ctype" in image/*) ;; *) fail "an image/* content type" "$ctype" ;; esac

step "create movie list"
json 201 POST /api/v1/lists -d "{\"name\":\"$RUN movies\",\"type\":\"movie\",\"folder_id\":\"$FOLDER_ID\"}"
MOVIE_LIST_ID="$(field .id)"

step "move item to a list of another type"
json 409 POST "/api/v1/items/$ITEM_ID/move" -H "If-Match: $ITEM_ETAG" -d "{\"list_id\":\"$MOVIE_LIST_ID\"}"
expect "problem code" type_mismatch "$(field .code)"

step "create a list and an item with no type"
json 201 POST /api/v1/lists -d "{\"name\":\"$RUN plain\",\"folder_id\":\"$FOLDER_ID\"}"
expect "no-type list type" null "$(jq -c '.type' "$T/body")"
PLAIN_LIST_ID="$(field .id)"
json 201 POST "/api/v1/lists/$PLAIN_LIST_ID/items" -d '{"title":"plain thing"}'
expect "no-type item type" null "$(jq -c '.type' "$T/body")"

step "search finds the item"
call 200 GET "/api/v1/search?q=$RUN%20big"
expect "search hits for the item" 1 "$(jq --arg id "$ITEM_ID" '[.data[] | select(.item.id == $id)] | length' "$T/body")"

step "trash the folder"
call 204 DELETE "/api/v1/folders/$FOLDER_ID" -H "If-Match: $FOLDER_ETAG"
BATCH_ID="$(trash_batch "$FOLDER_ID")"
[ -n "$BATCH_ID" ] || fail "the folder in /trash" "no trash entry"
call 200 GET /api/v1/tree
expect "folder gone from /tree" 0 "$(jq --arg id "$FOLDER_ID" '[.folders[] | select(.id == $id)] | length' "$T/body")"

step "restore the folder"
json 200 POST "/api/v1/trash/$BATCH_ID/restore"
expect "restored root" "$FOLDER_ID" "$(field .root_id)"
call 200 GET /api/v1/tree
expect "folder back in /tree" 1 "$(jq --arg id "$FOLDER_ID" '[.folders[] | select(.id == $id)] | length' "$T/body")"
expect "lists back in /tree" 4 "$(jq --arg id "$FOLDER_ID" '[.lists[] | select(.folder_id == $id)] | length' "$T/body")"

step "trash the folder again and purge it"
call 200 GET "/api/v1/folders/$FOLDER_ID"
call 204 DELETE "/api/v1/folders/$FOLDER_ID" -H "If-Match: $(etag)"
BATCH_ID="$(trash_batch "$FOLDER_ID")"
[ -n "$BATCH_ID" ] || fail "the folder in /trash" "no trash entry"
call 204 DELETE "/api/v1/trash/$BATCH_ID"
call 404 GET "/api/v1/items/$ITEM_ID"

echo "smoke ok"
