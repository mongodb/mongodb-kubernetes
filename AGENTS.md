# AGENTS.md — MongoDB Controllers for Kubernetes (MCK)

## `make precommit` rewrites tracked files

Not read-only. Rewrites `release.json`, `helm_chart/values.yaml`, CRDs, `public/*.yaml`.
Re-stage afterwards. Don't hand-edit versions — bump via
`scripts/evergreen/release/update_release.py`.

## venv

`./venv`, not `.venv` (uv not pip). `.venv` is silently ignored by repo scripts.

## `release.json` agent version overrides

OM variants read `supportedImages.mongodb-agent.opsManagerMapping.<om_version>`, **not** the
top-level `agentVersion`. Context files (`scripts/dev/contexts/variables/*`) can hardcode
`AGENT_VERSION`, overriding root-context. An empty `MDB_CUSTOM_AGENT_URL` silently falls
back to production — always verify the custom agent was actually downloaded.

## Skills

- Controller code (builders, multi-cluster, `MemberCluster.Index`, AppDB-not-mdbmulti) → `mck-code`
- Changelog entries → `mck-create-changelog`
- Local kind dev (context switching, `prepare-local-e2e`, running operator) → `mck-local-kind-dev`
