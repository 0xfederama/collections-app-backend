#!/usr/bin/env bash
# AT-18: every published port in the rendered compose config must bind 127.0.0.1.
set -euo pipefail
cd "$(dirname "$0")/.."

rendered="$(POSTGRES_PASSWORD=check docker compose config)"
published="$(grep -c 'published:' <<<"$rendered" || true)"
loopback="$(grep -c 'host_ip: 127.0.0.1' <<<"$rendered" || true)"

if [ "$published" != "$loopback" ]; then
  echo "compose publishes a port outside 127.0.0.1 ($published published, $loopback on loopback):" >&2
  grep -B3 -A3 'published:' <<<"$rendered" >&2
  exit 1
fi
echo "compose exposure ok ($published port(s), all on 127.0.0.1)"
