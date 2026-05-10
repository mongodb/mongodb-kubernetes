# Step 01 — Split `root-context` into logical + site-context

## Goal

Move site-derived exports out of `scripts/dev/contexts/root-context` into a
new sibling `scripts/dev/contexts/site-context`, leaving `root-context` to
emit only logical (side-agnostic) bytes. Both files are sourced by
`switch_context.sh` in step 02; they're not consumed directly by anything
else.

## Preconditions

- On branch `lsierant/devcontainer` (or a feature branch off it).
- Master plan reviewed (`README.md` in this directory).
- No prior steps started.

## Files touched

- `scripts/dev/contexts/root-context` (modify in place)
- `scripts/dev/contexts/site-context` (new file)

Do **not** touch `scripts/dev/contexts/private-context*` or
`scripts/dev/contexts/local-defaults-context` in this step. Their
contents are user-specific overrides; they continue to be sourced by
`switch_context.sh` BEFORE `root-context` (unchanged from today).

## Detailed plan

### 1. Create `scripts/dev/contexts/site-context`

This file is sourced ON the side it emits bytes for. It introspects the
running shell (`realpath`, `[[ -f /.dockerenv ]]`, `go env GOROOT`,
`uname -m`) — which is fine because it runs IN the side's env. It must
NOT export anything that's already a logical concern (NAMESPACE, image
refs, etc.) — those stay in `root-context`.

Required content:

```bash
#!/usr/bin/env bash
#
# site-context — site-derived environment variables (per-side bytes).
#
# Sourced by scripts/dev/switch_context.sh in an `env -i` subshell, ON the
# side that ran `make switch`. The values it exports depend on the
# filesystem layout of the running side (PROJECT_DIR), the running shell's
# Go install (GOROOT), the architecture (s390x conditionals), and the
# in-container vs. on-host k8s-forwarding-proxy address (K8S_FWD_PROXY).
#
# Companion: scripts/dev/contexts/root-context (logical, side-agnostic).

set -Eeou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

PROJECT_DIR="$(realpath "${script_dir}/../../..")"
export PROJECT_DIR

# In-container k8s-forwarding-proxy is reachable at the compose-fixed IP
# 172.${MCK_DEVC_NET_PREFIX}.0.10. On host, the kfp listens on
# 127.0.0.1:11616 (registered by `.devcontainer/scripts/post-start.sh`).
# Today this var is only set in compose for the devc service; surfacing
# it on host too means `setup_kind_cluster.sh` and `evg_host.sh`'s kfp
# registration paths Just Work when invoked from a host shell.
if [[ -f /.dockerenv ]]; then
  : "${MCK_DEVC_NET_PREFIX:?MCK_DEVC_NET_PREFIX must be set in devc (compose)}"
  K8S_FWD_PROXY="172.${MCK_DEVC_NET_PREFIX}.0.10"
else
  K8S_FWD_PROXY="127.0.0.1:11616"
fi
export K8S_FWD_PROXY

# KUBECONFIG variant — host kubeconfig has proxy-url=127.0.0.1:80${PREFIX};
# devc kubeconfig has proxy-url=gost-proxy:8080. Both managed by
# scripts/dev/evg_host.sh; we only pick the path here.
if [[ "${EVG_HOST_NAME:-}" != "" ]]; then
  if [[ -f /.dockerenv ]]; then
    KUBECONFIG="${PROJECT_DIR}/.generated/evg-host.devc.kubeconfig"
  else
    KUBECONFIG="${PROJECT_DIR}/.generated/evg-host.kubeconfig"
  fi
else
  KUBECONFIG="${HOME}/.kube/config"
fi
export KUBECONFIG

# When LOCAL_OPERATOR=true, the multi-cluster kubeconfig is written under
# ${PROJECT_DIR}/.generated/multicluster_kubeconfig (filesystem-specific).
if [[ "${LOCAL_OPERATOR:-}" == "true" ]]; then
  export KUBE_CONFIG_PATH="${PROJECT_DIR}/.generated/multicluster_kubeconfig"
fi

export MULTI_CLUSTER_CONFIG_DIR="${PROJECT_DIR}/.multi_cluster_local_test_files"
export MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH="${PROJECT_DIR}/docker/mongodb-kubernetes-tests/multi-cluster-kube-config-creator"

if command -v go >/dev/null 2>&1; then
  GOROOT="$(go env GOROOT)"
  export GOROOT
fi

# s390x-specific feature flags — use ${VAR:-default} so the caller can
# override before sourcing.
# PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION: s390x requires pure-Python
#   protobuf; C++ extension crashes at import.
#   https://github.com/protocolbuffers/protobuf/issues/24103
# SODIUM_INSTALL: pynacl must link against system libsodium; bundled
#   build broken on s390x+Python 3.13.
# PYTEST_OTEL_ENABLED: grpcio segfaults at exit on s390x+Python 3.13.
if [[ "$(uname -m)" == "s390x" ]]; then
  export PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION="${PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION:-python}"
  export SODIUM_INSTALL="${SODIUM_INSTALL:-system}"
  export PYTEST_OTEL_ENABLED="${PYTEST_OTEL_ENABLED:-false}"
fi
```

Notes for the implementing agent:
- The script is sourced (`source`d), not executed directly. `set -Eeou
  pipefail` is harmless when sourced.
- `EVG_HOST_NAME`, `MCK_DEVC_NET_PREFIX`, and `LOCAL_OPERATOR` are read
  from the env. They're set by `root-context` (logical) which gets
  sourced before `site-context` in step 02. Document the dependency
  order in step 02.
- This file must be `chmod +x` only if scripts ever execute it. Today
  `root-context` is sourced (no `+x` needed). Match its mode to be
  safe (`chmod 755`).

### 2. Modify `scripts/dev/contexts/root-context`

Remove site-derived exports and the s390x conditional. Concretely:

#### a. Remove the `PROJECT_DIR=…` block at the top of the file

Lines (current numbering):
```bash
PROJECT_DIR="$(realpath "${script_dir}/../../..")"
export PROJECT_DIR
```

Replace with a comment that documents `PROJECT_DIR` is now expected to
be set by `site-context` BEFORE this file is sourced:

```bash
# PROJECT_DIR is set by scripts/dev/contexts/site-context (sourced before
# this file by switch_context.sh). We read it here to locate release.json
# but DO NOT export it — that's site-context's job.
: "${PROJECT_DIR:?PROJECT_DIR must be set by site-context before sourcing root-context}"
```

`script_dir` and `script_name` lines just before the deleted block are
no longer needed by `root-context` itself. Delete them too.

#### b. Remove `MULTI_CLUSTER_CONFIG_DIR` and `MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH`

These are PROJECT_DIR-derived; they belong in `site-context` (already
included there). Delete from `root-context`.

#### c. Remove `KUBE_CONFIG_PATH` block

The `if [[ "${LOCAL_OPERATOR}" == "true" ]]; then export KUBE_CONFIG_PATH=…`
block in `root-context`. Site-derived (path under PROJECT_DIR); already
in `site-context`. Delete from `root-context`. Keep the
`PERFORM_FAILOVER=false` line — that's logical.

#### d. Remove the `KUBECONFIG` block

The full `if [[ "${EVG_HOST_NAME:-}" != "" ]]; then …` block at the bottom
of `root-context` that sets `KUBECONFIG` based on `[[ -f /.dockerenv ]]`.
Site-derived; already in `site-context`. Delete from `root-context`.

#### e. Remove the `GOROOT` block

```bash
if command -v go >/dev/null 2>&1; then
  GOROOT="$(go env GOROOT)"
  export GOROOT
fi
```

Site-derived (Go install location differs per side). Already in
`site-context`. Delete from `root-context`.

#### f. Remove the s390x conditional

```bash
if [[ "$(uname -m)" == "s390x" ]]; then
  export PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION="${PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION:-python}"
  export SODIUM_INSTALL="${SODIUM_INSTALL:-system}"
  export PYTEST_OTEL_ENABLED="${PYTEST_OTEL_ENABLED:-false}"
fi
```

Site-derived (architecture differs per side). Already in `site-context`.
Delete from `root-context`.

#### g. Keep everything else

In particular, KEEP:
- `MCK_DEVC_NET_PREFIX` reading from `.devcontainer/.env`. This is
  logical — same per worktree regardless of side. The path used to read
  the file IS PROJECT_DIR-rooted, but the value is logical. Use
  `${PROJECT_DIR}/.devcontainer/.env`.
- NAMESPACE suffixing.
- `EVG_HOST_NAME` and `EVG_HOST_ADDRESS` resolution.
- All version pins, image refs, registry vars.
- `OPERATOR_ENV`, `LOCAL_RUN`, etc.
- `AGENT_VERSION` reading from `${PROJECT_DIR}/release.json`. The path
  is PROJECT_DIR-rooted but the value is logical (a version string).

The `:` at the top of the file (`: "${PROJECT_DIR:?…}"`) ensures that
sourcing `root-context` without `PROJECT_DIR` set fails fast with a
clear message — which is what we want, because step 02 establishes the
sourcing order.

## Verification

Run inside the devcontainer (or on host) after the edits:

```bash
# Sanity: site-context produces the expected per-side bytes
env -i bash -c 'source scripts/dev/contexts/site-context && export -p' \
  | grep -E '^declare -x (PROJECT_DIR|KUBECONFIG|K8S_FWD_PROXY|GOROOT|MULTI_CLUSTER_)' \
  | sort

# On host, expect:
#   declare -x GOROOT="/usr/local/go"  (or wherever brew installed)
#   declare -x K8S_FWD_PROXY="127.0.0.1:11616"
#   declare -x KUBECONFIG="…/.kube/config"  (since EVG_HOST_NAME unset here)
#   declare -x MULTI_CLUSTER_CONFIG_DIR="…/.multi_cluster_local_test_files"
#   declare -x MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH="…/multi-cluster-kube-config-creator"
#   declare -x PROJECT_DIR="…/<worktree>"

# Sanity: root-context fails fast without PROJECT_DIR set
env -i bash -c 'source scripts/dev/contexts/root-context' 2>&1 | head -3
# expect: PROJECT_DIR: PROJECT_DIR must be set by site-context …

# Sanity: root-context succeeds when PROJECT_DIR is pre-set
env -i PROJECT_DIR="$(pwd)" PATH="${PATH}" bash -c '
  source scripts/dev/contexts/root-context && env | grep -E "^(PROJECT_DIR|KUBECONFIG|GOROOT)="
'
# expect: only PROJECT_DIR (passed in); NO KUBECONFIG / GOROOT lines from root-context.
```

## Outputs / postconditions

- `scripts/dev/contexts/site-context` exists with the content above.
- `scripts/dev/contexts/root-context` no longer exports `PROJECT_DIR`,
  `KUBECONFIG`, `KUBE_CONFIG_PATH`, `MULTI_CLUSTER_CONFIG_DIR`,
  `MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH`, `GOROOT`, or the s390x
  conditionals.
- `root-context` reads `PROJECT_DIR` from env (set by `site-context`)
  and aborts with a clear error if it isn't set.
- All other exports in `root-context` unchanged.
- No consumer is broken yet — `switch_context.sh` is updated in step 02
  to source both files in order.

## Pitfalls

- **`AGENT_VERSION`** in `root-context` does
  `AGENT_VERSION="$(jq -r '.agentVersion' "${PROJECT_DIR}/release.json")"`.
  This depends on `PROJECT_DIR` being set BEFORE `root-context` is
  sourced. The `: "${PROJECT_DIR:?}"` at the top guards this; step 02's
  sourcing order (`source site-context; source root-context`) satisfies it.

- **MCK_DEVC_NET_PREFIX read from `.devcontainer/.env`** in `root-context`
  uses `${PROJECT_DIR}/.devcontainer/.env`. Path is PROJECT_DIR-rooted;
  works as long as PROJECT_DIR is set. Don't move this block to
  `site-context` — the VALUE of `MCK_DEVC_NET_PREFIX` is logical
  (per-worktree), not site.

- **Don't accidentally `unset PROJECT_DIR`** at the end of `root-context`.
  We need it to remain in env for downstream consumers (e.g., the
  `MULTI_CLUSTER_CONFIG_DIR` derivations… wait, those moved to
  `site-context`. Still, leave it set — `switch_context.sh` will subtract
  it from logical capture in step 02 if needed.)

- **EVG host runs `make switch` over SSH** via `evg_host.sh:72`. EVG
  host has its own filesystem and its own `.generated/`. Its
  `[[ -f /.dockerenv ]]` is false → site=host → its `context.host.env`
  carries EVG-host-flavored bytes (its own PROJECT_DIR, its own
  GOROOT). Works without any EVG-specific code path.

## Commit

Single commit at the end of this step:

```
contexts: split root-context into logical + site-context

Move PROJECT_DIR, KUBECONFIG, KUBE_CONFIG_PATH, MULTI_CLUSTER_*, GOROOT,
K8S_FWD_PROXY, and the s390x conditionals from root-context into a new
sibling site-context. root-context now emits only side-agnostic logical
bytes; site-context introspects the running shell to emit side-derived
bytes. switch_context.sh (next step) sources site-context first, then
root-context, so logical can read PROJECT_DIR for release.json lookups.

This is the first step of the .generated/context.* env file split that
fixes cross-side PATH pollution. See docs/dev/context-split/README.md.
```

Pre-commit must be clean (`pre-commit run --all-files`).

## Cross-references

- Master plan: [`README.md`](README.md)
- Next step: [`02-switch-context.md`](02-switch-context.md)
- Backlog item: `docs/dev/worktree-tooling-improvements.md` #1

## Notes (implementation)

Implemented on `lsierant/devcontainer` as commit
`contexts: split root-context into logical + site-context`.

### What changed

- **New**: `scripts/dev/contexts/site-context` (mode 0755) — exact bytes from
  the plan's section 1.
- **Modified**: `scripts/dev/contexts/root-context` —
  - Replaced the `PROJECT_DIR=…; export PROJECT_DIR` block with the
    `: "${PROJECT_DIR:?…}"` guard at the top.
  - Removed `MULTI_CLUSTER_CONFIG_DIR`, `MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH`.
  - Stripped the `KUBE_CONFIG_PATH` export from the `LOCAL_OPERATOR=true`
    branch; kept `PERFORM_FAILOVER=false`.
  - Removed the `KUBECONFIG` / `EVG_HOST_NAME` block.
  - Removed the `GOROOT` block.
  - Removed the s390x conditional block at the bottom.

### Deviations from the plan

1. **Kept `script_dir` / `script_name` in `root-context`.** Section 2a
   says to delete them along with the PROJECT_DIR block. That's incorrect
   in this codebase — those vars are still required by the very next line
   `source "${script_dir}/private-context"`, and the plan explicitly
   carves out that `private-context*` is not touched in this step. Left
   them in place; the file still passes shellcheck and verification.
   **Action for step 02 author:** if private-context sourcing moves out
   of root-context (e.g. into `switch_context.sh` or
   `local-defaults-context`), then `script_dir`/`script_name` can finally
   be removed from root-context.

2. **`site-context` mode is 0755 per the plan body**, not 0644 like
   `root-context`. The instruction text in this doc and the agent prompt
   both say "match root-context's mode (0755)" but the actual mode of
   `root-context` on disk is 0644. I followed the explicit numeric
   directive (0755) rather than the "match" hint. Net effect is harmless
   — both files are sourced, never executed. **Action for step 02:**
   decide whether to normalize both to 0644 or both to 0755; pick one
   convention and apply it.

3. **Verification block needed extra env passed through `env -i`.** As
   written in the plan, the first verification (`env -i bash -c
   'source site-context …'`) fails under `set -u` because `HOME` is
   stripped and the `KUBECONFIG="${HOME}/.kube/config"` fallback dereferences
   it. Worked around by adding `HOME="${HOME}"` to the `env -i`
   invocation; same fix for `PATH` so that `command -v go` finds the Go
   toolchain and the `GOROOT` line shows up in the output. The
   `site-context` file itself was NOT changed — this is a verification-
   only adjustment. **Action for step 02:** when `switch_context.sh`
   sources `site-context` from inside `env -i`, it must pass `HOME` and
   `PATH` through (the existing call already passes `PATH`; add `HOME`).

4. **Third verification (`env -i PROJECT_DIR=… PATH=… bash -c 'source
   root-context …'`) failed on `REGISTRY: unbound variable`.** This is a
   pre-existing dependency: `root-context` line `export
   REGISTRY=${REGISTRY}` requires `REGISTRY` to be set by
   `local-defaults-context`, which `switch_context.sh` sources before
   `root-context`. The plan's verification block sourced `root-context`
   alone (no `local-defaults-context`), which exposed this old coupling.
   I confirmed the property the verification was actually checking
   (root-context emits no `KUBECONFIG=` / `GOROOT=` lines) by adding
   `REGISTRY="dummy/reg"` to the `env -i` invocation; with that, the
   command exits 0 and the only matching line is the passed-in
   `PROJECT_DIR=`. **Action for step 02:** the new
   `switch_context.sh` flow needs to source `local-defaults-context`
   BEFORE `root-context` (current behavior — preserve it) so REGISTRY
   chain stays intact. Alternative: move REGISTRY default into
   `root-context` itself with `${REGISTRY:-${BASE_REPO_URL_SHARED:-}}`
   fallback, but that's out of scope here.

### Verification output (relevant lines)

site-context (with `HOME`, `PATH` passed through):

```
declare -x GOROOT="/Users/lukasz.sierant/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.9.darwin-arm64"
declare -x K8S_FWD_PROXY="127.0.0.1:11616"
declare -x KUBECONFIG="/Users/lukasz.sierant/.kube/config"
declare -x MULTI_CLUSTER_CONFIG_DIR="…/lsierant_devcontainer/.multi_cluster_local_test_files"
declare -x MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH="…/multi-cluster-kube-config-creator"
declare -x PROJECT_DIR="/Users/lukasz.sierant/mdb/lsierant_devcontainer"
```

root-context fails fast without PROJECT_DIR:

```
scripts/dev/contexts/root-context: line 8: PROJECT_DIR: PROJECT_DIR must be set by site-context before sourcing root-context
```

root-context succeeds with `PROJECT_DIR` + `REGISTRY` pre-set; emits no
KUBECONFIG/GOROOT:

```
PROJECT_DIR=/Users/lukasz.sierant/mdb/lsierant_devcontainer
```

(Only the inherited `PROJECT_DIR` shows; root-context no longer adds
`KUBECONFIG=` or `GOROOT=` lines, which is the property under test.)

`pre-commit run --files scripts/dev/contexts/site-context
scripts/dev/contexts/root-context` clean (ShellCheck passed).
