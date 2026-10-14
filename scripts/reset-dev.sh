#!/usr/bin/env bash
# Deletes the dev containers and the pgdata/appdata volumes. Refuses in production.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  # shellcheck source=/dev/null
  set -a && . ./.env && set +a
fi
if [ "${APP_ENV:-development}" = "production" ]; then
  echo "refusing to reset: APP_ENV=production" >&2
  exit 1
fi

read -r -p "This deletes the dev database and uploaded files. Type 'reset' to continue: " answer
if [ "$answer" != "reset" ]; then
  echo "aborted"
  exit 1
fi
docker compose down --volumes --remove-orphans
rm -rf ./data
echo "dev data deleted"
