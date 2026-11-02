# smoke

## Purpose

HTTP smoke test of a running stack: one folder, two lists, items, image upload and thumbnail, search, trash, restore and purge, checked with `curl` and `jq`. Run against `./deploy/dev up -d --fresh`; never against `./deploy/test` (the user's manual environment). It is not part of `make check`.

## Key files

- `scripts/smoke.sh`: the whole test; `step`/`expect`/`call` helpers, EXIT trap cleanup
- `Makefile`: `smoke` target (runs `./scripts/smoke.sh`)

## Interfaces

- `make smoke`: runs against `API_URL`, default `http://localhost:8090`
- `API_URL=<base> scripts/smoke.sh`: base URL without `/api/v1` (trailing slash is stripped)
- Exit 0 prints `smoke ok`; otherwise exit 1 prints the failing step and the expected/got values
- Needs `curl`, `jq`, `base64` on the host

## Gotchas

- Start the stack first: `./deploy/dev up -d --fresh` (ports 5183/8090).
- Every name carries a `smoke-<pid>-<time>` prefix. Cleanup trashes the test folder and purges trash batches whose `root_id` is that folder, so only the first 200 trash entries are scanned.
- Cleanup runs on any exit, including Ctrl-C and TERM, and is best effort: a failed cleanup leaves the `smoke-…` folder active or in the trash.
- The 8x8 PNG is embedded as base64 in the script; no fixture file is needed.
- Not wired into `make check` (no CI, D-031); run it when the change touches the HTTP flow.
