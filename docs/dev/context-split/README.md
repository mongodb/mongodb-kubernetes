# Per-side context env split — master plan

This plan refactors the `.generated/context.*` env file generation to fix a
class of cross-site PATH/PROJECT_DIR pollution bugs and to make host vs.
devcontainer execution clean by construction.

Prepared in a planning session on 2026-05-10. To be executed in a fresh
session via subagents (one per step in `01-…` through `06-…`). Each step
file is self-contained; the implementing agent does NOT need this
conversation's history.

Parent backlog item: `docs/dev/worktree-tooling-improvements.md` #1
("`op_run.sh` shouldn't inherit host-side `PATH` from `context.export.env`").
This plan supersedes the original "awk-filter" workaround sketched in that
doc with a structural refactor.

---

## 1. Problem

Today `scripts/dev/switch_context.sh` runs `scripts/dev/contexts/root-context`
in an `env -i` subshell on whichever side ran `make switch`. The captured
`export -p` output gets written to `.generated/context.env` (vanilla) and
re-prefixed to `.generated/context.export.env` (with `export`). The file
contains:

- Logical bytes (NAMESPACE, OM creds, image refs, watch resources, …) —
  **same regardless of side**.
- Site-derived bytes (PROJECT_DIR, KUBECONFIG path, GOROOT, K8S_FWD_PROXY
  via compose, PATH inherited from the running shell, …) — **different per
  side**.

When the host runs `make switch` first, then a devcontainer shell sources
`context.export.env`, it inherits a macOS PATH (`/opt/homebrew/...`,
`/Users/.../bin`) that doesn't exist in the container, clobbering the
container's `/usr/local/go/bin:/go/bin:...`. Result: `go: command not
found`, `which kubectl` misses, etc. Symmetric bug exists in the reverse
direction.

The current safety net is a `## Side: host|container` stamp in
`context.env` and a `fatal` in `set_env_context.sh` when sourced on the
wrong side — but most consumers source `context.export.env` directly,
bypassing the guard.

## 2. Final design (locked)

Generated artifacts in `.generated/`:

| File | Content | Written by |
|---|---|---|
| `context.env` | logical only (NAMESPACE, image refs, watch resources, …) | every `make switch`, identical bytes from any side |
| `context.host.env` | site bytes for host (PROJECT_DIR=/Users/…, KUBECONFIG=…, K8S_FWD_PROXY=127.0.0.1:11616, GOROOT=…, MULTI_CLUSTER_*, s390x conditionals) | `make switch` run on host |
| `context.devc.env` | site bytes for devc (PROJECT_DIR=/workspace, KUBECONFIG=…, K8S_FWD_PROXY=172.X.0.10, …) | `make switch` run in devc |
| `context.operator.env` | logical operator overlay (output of `print_operator_env.sh` minus `KUBECONFIG`/`KUBE_CONFIG_PATH`) | every `make switch` |

**Files removed:**
- `.generated/context.export.env` (was logical with `export` prefix; vanilla
  `context.env` + `set -a` replaces it)
- `.generated/context.operator.export.env` (same)
- `## Side:` stamp in `context.env`
- `set_env_context.sh`'s side-mismatch `fatal`

**Side detection:** `[[ -f /.dockerenv ]] && side=devc || side=host`.
Single rule used in `switch_context.sh`, `scripts/dev/devenv`, `main.go`,
and `conftest.py`. No `MCK_SITE` env var — `/.dockerenv` is sufficient and
zero-config on host.

**Sourcing pattern (`scripts/dev/devenv`, sourceable):**
```bash
side=$([[ -f /.dockerenv ]] && echo devc || echo host)
# loud-fail if expected files missing
set -a
. .generated/context.env
. ".generated/context.${side}.env"
set +a
[[ -f venv/bin/activate ]] && . venv/bin/activate
```

`devenv` does **not** modify PATH. `${PROJECT_DIR}/bin` is added to PATH
via `/etc/profile.d/mck-bin.sh` in the devcontainer image (container side).
On host, devs add it to `~/.zshrc` themselves.

**Operator + pytest** load three files in order with override semantics:
```
.generated/context.env
.generated/context.<side>.env
.generated/context.operator.env
```
Site detection in code: `os.Stat("/.dockerenv")` (Go) /
`Path("/.dockerenv").exists()` (Python). Missing files log a warning,
don't abort.

## 3. Variable classification

### Logical → `context.env`

**Identity / context**
- `NAMESPACE`, `WATCH_NAMESPACE`, `MCK_DEVC_NET_PREFIX`
- `EVG_HOST_NAME`, `EVG_HOST_ADDRESS`
- `OPERATOR_NAME`, `OPERATOR_ENV`, `CURRENT_VARIANT_CONTEXT`
- `workdir` (default)

**Cluster topology / behavior**
- `TEST_POD_CLUSTER`, `CENTRAL_CLUSTER`
- `KUBE_ENVIRONMENT_NAME`, `CLUSTER_TYPE`, `CLUSTER_DOMAIN`,
  `OPERATOR_CLUSTER_SCOPED`
- `MULTI_CLUSTER_CREATE_SERVICE_ACCOUNT_TOKEN_SECRETS`,
  `PERFORM_FAILOVER`

**Versions**
- `AGENT_VERSION`, `OPERATOR_VERSION`, `INIT_DATABASE_VERSION`,
  `INIT_OPS_MANAGER_VERSION`, `DATABASE_VERSION`,
  `READINESS_PROBE_VERSION`, `VERSION_UPGRADE_HOOK_VERSION`,
  `OLM_VERSION`, `PYTHON_VERSION`, `HELM_VERSION`, `KUBECTL_VERSION`,
  `MDB_SEARCH_VERSION`

**Image refs**
- All `*_REGISTRY` vars (REGISTRY, QUAY_REGISTRY, OPERATOR_REGISTRY,
  INIT_*_REGISTRY, OPS_MANAGER_REGISTRY, DATABASE_REGISTRY,
  MDB_AGENT_REGISTRY, MEKO_TESTS_REGISTRY, READINESS_PROBE_REGISTRY,
  VERSION_UPGRADE_HOOK_REGISTRY)
- All `*_IMAGE_REPOSITORY` vars
- `AGENT_IMAGE`, `READINESS_PROBE_IMAGE`,
  `VERSION_UPGRADE_HOOK_IMAGE`, `MONGODB_REPO_URL`,
  `MONGODB_ENTERPRISE_DATABASE_IMAGE`

**Community / Search**
- `MDB_COMMUNITY_*`, `MDB_SEARCH_*`, `MDB_ENVOY_IMAGE`

**OM endpoints**
- `OPS_MANAGER_NAMESPACE`, `OM_HOST`, `OM_BASE_URL`,
  `e2e_cloud_qa_baseurl`, `e2e_cloud_qa_baseurl_static_2`

**Misc behavior**
- `LOCAL_RUN`, `IMAGE_TYPE`, `UBI_IMAGE_WITHOUT_SUFFIX`,
  `MDB_MAX_CONCURRENT_RECONCILES`, `SIGNING_PUBLIC_KEY_URL`

**Release**
- `RELEASE_INITIAL_COMMIT_SHA`, `RELEASE_INITIAL_VERSION`

### Site → `context.<side>.env`

- `PROJECT_DIR` (host: `/Users/.../<worktree>`; devc: `/workspace`)
- `workdir` (deprecated alias for `PROJECT_DIR`; retained for
  evergreen scripts)
- `KUBECONFIG` (host: `${PROJECT_DIR}/.generated/evg-host.kubeconfig`;
  devc: `${PROJECT_DIR}/.generated/evg-host.devc.kubeconfig`)
- `KUBE_CONFIG_PATH`: `${PROJECT_DIR}/.generated/multicluster_kubeconfig`
  (set unconditionally; only consulted by the operator when
  `LOCAL_OPERATOR=true`)
- `MULTI_CLUSTER_CONFIG_DIR`
  (`${PROJECT_DIR}/.multi_cluster_local_test_files`)
- `MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH`
  (`${PROJECT_DIR}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator`)
- `MDB_OM_VERSION_MAPPING_PATH` (`${PROJECT_DIR}/release.json`)
- `ENV_FILE` (`${PROJECT_DIR}/.ops-manager-env`)
- `NAMESPACE_FILE` (`${PROJECT_DIR}/.namespace`)
- `DEFAULT_HELM_CHART_PATH` (`${PROJECT_DIR}/helm_chart`)
- `GOPATH` (`${HOME}/gosd` default; private-context override
  preserved per side via the subtraction)
- `GOROOT` (`go env GOROOT` differs per side)
- `K8S_FWD_PROXY` (host: `127.0.0.1:11616`;
  devc: `172.${MCK_DEVC_NET_PREFIX}.0.10`) — **new addition** to host file
- `PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION`, `SODIUM_INSTALL`,
  `PYTEST_OTEL_ENABLED` (s390x conditionals; only set on `uname -m == s390x`)

### Not in any file

- `PATH` — never written. Container's `/etc/profile.d/mck-bin.sh` prepends
  `/workspace/bin`. Host: dev-managed.
- `VIRTUAL_ENV` — set by `. venv/bin/activate` in `devenv`.

### Operator overlay → `context.operator.env`

Output of `scripts/dev/print_operator_env.sh` with `^KUBECONFIG=` and
`^KUBE_CONFIG_PATH=` lines stripped (those come from `context.<side>.env`).
No per-side operator overlay file.

## 4. Constraints honored

- **godotenv compatibility.** `.env` files stay vanilla `KEY=VALUE` with no
  shell logic. Verified consumers: `main.go:547` (Go's `joho/godotenv`),
  `docker/mongodb-kubernetes-tests/tests/conftest.py:29`
  (Python's `python-dotenv`).
- **Default workflow is host.** Operator and pytest run primarily on host;
  devc is opt-in. No required host-side env-var setup; `[[ -f /.dockerenv ]]`
  detection works without configuration.
- **`source` muscle memory.** Devs source via `. scripts/dev/devenv`
  (sourceable bootstrap) or `mck-env` (shell function defined in
  shell-init.sh / dev's `~/.zshrc`). The verbose `set -a` dance is hidden.
- **Logical bytes identical across sides.** By splitting logical from site,
  any side's `make switch` produces the same `context.env` bytes; site
  bytes never leak into the shared file.
- **EVG host.** Inherits "host" semantics with no special code path
  (`/.dockerenv` absent → side=host).

## 5. Step order and dependencies

```
01 (split root-context)
     │
     ▼
02 (switch_context.sh) ──┬──► 03 (devenv bootstrap)
                         │
                         ├──► 04 (godotenv consumers)
                         │
                         ▼
                    05 (shell consumers)
                         │
                         ▼
                    06 (verification)
```

- 01 must complete before 02 (switch_context invokes both contexts).
- 02 produces the new file artifacts; 03/04/05 consume them.
- 03 and 04 are independent of each other; can run in parallel.
- 05 depends on both 03 (devenv must exist) and 04 (consumers updated so
  shell scripts that re-source operator's env get coherent results).
- 06 is the end-to-end check.

Each step lands as its own commit. Push as a small stack on
`lsierant/devcontainer` per the standing rule (devcontainer/orchestrator
fixes land directly; no JIRA tickets; cascade-rebase descendants).

## 6. Standing rules

- Branch: `lsierant/devcontainer` (no JIRA ticket; this is orchestrator
  work).
- Commits: one per step. Conventional message style ("module: short
  imperative summary; explain why in the body").
- Pre-commit: `pre-commit run --all-files` clean before each commit.
- Validation: each step has its own verification block; run before
  proceeding to the next step.
- Failure: if a step's verification fails, stop and surface to the user.
  Do not paper over with workarounds.
- Rebase: after the stack lands, cascade-rebase any descendant branches
  (the user has a list via git machete in main repo's `.git/machete`).

### 6a. Manual testing requirement (do NOT automate yet)

Before declaring this rollout complete, the implementing agent MUST
exercise the change end-to-end on a freshly bootstrapped stack:

1. Use the `worktree-dev` skill (in
   `core-platforms-ai-tools-mck-dev-rules`) to spin up a fresh
   sibling worktree + EVG host + devcontainer stack. Don't reuse an
   existing stack — the cold-start paths (first `make switch` on
   each side, `on-create` populating `context.devc.env`,
   `/etc/profile.d/mck-bin.sh` taking effect on first container
   shell) are part of what's being verified.

2. Walk through the full V1–V10 verification block in
   [`06-verification.md`](06-verification.md) **manually** (don't
   wrap in a script, don't bake into a CI job). The point is to
   catch UX surprises a script wouldn't notice — error messages
   that mislead, ordering issues in shell-init, missing PATH
   entries that only show up when a human tries `kubectl get pods`,
   etc.

3. Try at least two `make switch context=…` flips (e.g.,
   `e2e_mdb_kind_ubi_cloudqa` → `root-context` → back) and
   re-source via `mck-env` after each, on both host and devc.
   Verify env vars update where expected, stay where expected.

4. Open a fresh devc shell after each side does its switch and
   confirm `mck-env` (or auto-source via shell-init.sh) loads the
   right bytes without the dev typing anything.

5. Tear down the test stack via `wt_teardown.sh` once verification
   is done, so the next round (or another developer's parallel work)
   isn't blocked by EVG host capacity.

**Do NOT automate this test** in this rollout. After it lands and
soaks for a release cycle or two, we may revisit and decide whether
parts of V1–V10 are worth a `make test-context-split` target or a
`bats` test in `scripts/test/bash/`. That's a follow-up — not a
gating requirement here.

The reason for keeping this manual: the test exercises the *human
ergonomic* path (loud error messages, mck-env muscle memory, PATH
resolution under interactive shells, etc.), and a passing automated
script can hide regressions in those very surfaces.

## 7. Rejected alternatives (do not revisit without strong reason)

- **`awk` filter in `op_run.sh`** to selectively source non-PATH vars from
  the existing `context.export.env`. Treats symptom; doesn't fix the
  shared-file design.
- **Per-side directories `.generated-host/` / `.generated-devc/` with
  `.generated/` as compat mirror.** Compat mirror reproduces the
  last-writer-wins bug.
- **Single file per site (`context.host.env` containing both logical and
  site bytes; same for devc).** Drifts logical bytes between sides; loses
  the "logical bytes identical" invariant.
- **Named volume overlay at `.generated/site/` for unified sourcing.**
  Linux native Docker volume-ownership concerns + cold-start timing
  fragility outweigh the cosmetic "one source statement" win, since the
  bootstrap hides the difference anyway.
- **`MCK_SITE` env var as primary side selector.** `/.dockerenv` is
  sufficient, zero-config, and works in all spawning paths
  (compose, `devcontainer exec`, `docker exec`).
- **Conditional shell logic in `.env` files.** Breaks godotenv-style
  parsers (Go and Python both call out KEY=VALUE only).
- **Wholesale source of `context.export.env` with `bash -lc`** to let
  `/etc/profile.d/` re-set PATH after the source. Brittle; depends on
  source-order in the inner shell.

## 8. Step files

- [`01-root-context.md`](01-root-context.md) — split `root-context` into
  logical-only + new `site-context`.
- [`02-switch-context.md`](02-switch-context.md) — refactor
  `switch_context.sh` to write `context.env` (logical, every side) +
  `context.<side>.env` (site, current side only); drop `.export.env` and
  `## Side:` stamp; emit logical-only `context.operator.env`.
- [`03-devenv-bootstrap.md`](03-devenv-bootstrap.md) — new
  `scripts/dev/devenv` sourceable; `mck-env` shell function in
  shell-init.sh; `/etc/profile.d/mck-bin.sh` in Dockerfile for
  `${PROJECT_DIR}/bin` PATH; document host install.
- [`04-godotenv-consumers.md`](04-godotenv-consumers.md) — update
  `main.go:547` and `conftest.py:29` for multi-file
  `Overload`/`override=True` with side detection via `/.dockerenv`.
- [`05-shell-consumers.md`](05-shell-consumers.md) — migrate `op_run.sh`,
  `e2e_run.sh`, `set_env_context.sh`, shell-init.sh, `evg_host.sh`,
  others to use the new bootstrap pattern.
- [`06-verification.md`](06-verification.md) — end-to-end verification on
  host + in devc.
