# AGENTS.md — MongoDB Controllers for Kubernetes (MCK)

## `make precommit` rewrites tracked files

Not read-only. Rewrites `release.json`, `helm_chart/values.yaml`, CRDs, `public/*.yaml`
and auto-stages them. Don't hand-edit versions — bump via
`scripts/evergreen/release/update_release.py`.

## CRDs are sentinel-gated on `api/*.go`

`make manifests` (and the precommit `generate-manifests` hook) only regenerate CRDs when
`api/*.go` changes. Hand-editing `helm_chart/crds/` or `public/crds.yaml` without touching
`api/` → `make manifests` is a no-op, precommit passes, wrong CRDs ship. Edit `api/` types
and let `controller-gen` produce them.

## venv

`./venv`, not `.venv` (uv not pip). `.venv` is silently ignored by repo scripts.

## `release.json` agent version overrides

OM variants read `supportedImages.mongodb-agent.opsManagerMapping.<om_version>`, **not** the
top-level `agentVersion`. Context files (`scripts/dev/contexts/variables/*`) can hardcode
`AGENT_VERSION`, overriding root-context. An empty `MDB_CUSTOM_AGENT_URL` silently falls
back to production — always verify the custom agent was actually downloaded.
