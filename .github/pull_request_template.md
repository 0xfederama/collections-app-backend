## What & why
<!-- Task ID from IMPLEMENTATION_PLAN (e.g. B-07) and a short summary -->

## How to verify
<!-- Paste the output of `make check` / `npm run check` plus the manual verification (no CI, D-031) -->

## Checklist
- [ ] `make check` / `npm run check` passes locally
- [ ] Tests added/updated, and run by the main session (AT ids: …)
- [ ] `CLAUDE.md` updated if commands/layout/conventions/gotchas changed
- [ ] `docs/` updated by docs-keeper
- [ ] No scope creep (ORCHESTRATOR_BRIEF §2); no secrets committed
- [ ] Migration: up/down/up tested; destructive? → backup warning below
- [ ] Deviations from design docs recorded in DECISIONS.md (D-…)

## Contract change (backend only, if `api/openapi.yaml` changed)
<!-- Affected endpoints, additive vs breaking, rollout order, frontend follow-up task, tag to cut -->
