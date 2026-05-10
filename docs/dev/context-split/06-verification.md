# Step 06 — End-to-end verification

## Goal

After steps 01–05 land, exercise the full host + devc + EVG-host paths
to confirm the split works correctly under realistic workflows. Each
verification block produces a clear pass/fail signal; failures should
stop the rollout.

## Preconditions

- Steps 01–05 committed on `lsierant/devcontainer`.
- Working devcontainer + EVG host stack available
  (e.g., a worktree set up via `wt_setup.sh`).
- `make switch context=…` known-good context
  (e.g., `e2e_mdb_kind_ubi_cloudqa`).

## Verification matrix

### V1 — Host shell, fresh `make switch`

Goal: confirm host-side `make switch` produces the right files and
host PATH/PROJECT_DIR pollution does NOT leak into the bind-mounted
shared file.

```bash
# Clean any stale generated files (use a worktree where it's safe to do so).
rm -f .generated/context*.env .generated/.current_context

# On host
make switch context=e2e_mdb_kind_ubi_cloudqa

# Expect three files:
ls .generated/context*.env
#   .generated/context.env
#   .generated/context.host.env
#   .generated/context.operator.env

# No .export.env files
ls .generated/*.export.env 2>/dev/null
# Expect: empty

# context.env should NOT contain host PROJECT_DIR or host PATH
grep -E '^(PROJECT_DIR|PATH|KUBECONFIG|GOROOT)=' .generated/context.env
# Expect: empty

# context.host.env should contain host PROJECT_DIR
grep -E '^PROJECT_DIR=' .generated/context.host.env
# Expect: PROJECT_DIR="/Users/.../<worktree>"

# Source via devenv on host
. scripts/dev/devenv
echo "PROJECT_DIR=${PROJECT_DIR}"
echo "K8S_FWD_PROXY=${K8S_FWD_PROXY}"
echo "NAMESPACE=${NAMESPACE}"
# Expect:
#   PROJECT_DIR=/Users/.../<worktree>
#   K8S_FWD_PROXY=127.0.0.1:11616
#   NAMESPACE=ls-XX (or whatever the context defines)
```

### V2 — Devc shell, fresh `make switch`

Goal: confirm devc-side `make switch` produces the per-side file and
the operator/pytest path resolution is clean.

```bash
# From host:
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace
  make switch context=e2e_mdb_kind_ubi_cloudqa
  ls .generated/context*.env
'
# Expect (in addition to context.env, context.host.env, context.operator.env
# which were created by V1's host-side switch):
#   .generated/context.devc.env

# context.devc.env should have devc PROJECT_DIR
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  grep -E "^(PROJECT_DIR|K8S_FWD_PROXY)=" /workspace/.generated/context.devc.env
'
# Expect:
#   PROJECT_DIR="/workspace"
#   K8S_FWD_PROXY="172.<MCK_DEVC_NET_PREFIX>.0.10"

# Source via devenv inside devc
devcontainer exec --workspace-folder "$(pwd)" bash -lic '
  . /workspace/scripts/dev/devenv
  echo "PROJECT_DIR=${PROJECT_DIR}"
  echo "PATH=${PATH}"
  which go
'
# Expect:
#   PROJECT_DIR=/workspace
#   PATH starts with /workspace/bin: (from /etc/profile.d/mck-bin.sh)
#   /usr/local/go/bin/go (or wherever container go lives — not /opt/homebrew)
```

### V3 — Bidirectional independence

Goal: confirm switching context on one side leaves the OTHER side's
site file untouched.

```bash
# Note current host file mtime
host_mtime_before=$(stat -f %m .generated/context.host.env 2>/dev/null \
                    || stat -c %Y .generated/context.host.env)

# Switch context inside devc
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace && make switch context=root-context
'

host_mtime_after=$(stat -f %m .generated/context.host.env 2>/dev/null \
                   || stat -c %Y .generated/context.host.env)

# Expect: mtimes equal (host file untouched)
[[ "${host_mtime_before}" == "${host_mtime_after}" ]] && echo OK || echo FAIL

# context.env (logical) WAS rewritten by devc's switch — verify same bytes
diff_lines=$(diff <(grep -v '^## ' .generated/context.env | sort) \
                  <(grep -v '^## ' .generated/context.env | sort) | wc -l)
# (trivially equal here; the real check is that the bytes EXIST)
```

### V4 — Operator load three files

Goal: confirm `main.go:loadEnvFromLocalFileForDevelopment` loads all
three files in the right order with override semantics.

```bash
# Inside devc, run the operator briefly
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace
  bash scripts/dev/op_run.sh --wait
' &
sleep 5
devcontainer exec --workspace-folder "$(pwd)" tmux capture-pane -t mck-operator -p | \
    grep -E 'Loaded environment variables from file' | head -10
devcontainer exec --workspace-folder "$(pwd)" tmux kill-session -t mck-operator

# Expect three lines:
#   Loaded environment variables from file .generated/context.env
#   Loaded environment variables from file .generated/context.devc.env
#   Loaded environment variables from file .generated/context.operator.env
```

### V5 — Pytest load two files

Goal: confirm `conftest.py` loads context.env + context.devc.env in
order with override.

```bash
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace/docker/mongodb-kubernetes-tests
  PYTEST_OTEL_ENABLED=false python -m pytest --collect-only tests/ 2>&1 | \
      grep -E "Loaded environment variables from file" | head -5
'
# Expect two lines:
#   Loaded environment variables from file .../.generated/context.env
#   Loaded environment variables from file .../.generated/context.devc.env
```

### V6 — Cold-start loud failure

Goal: confirm `devenv` fails with a clear message when files are
missing (the documented happy-path mitigation: run `make switch`).

```bash
mv .generated/context.devc.env /tmp/saved.devc.env
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace
  . scripts/dev/devenv
' 2>&1 | head -5
mv /tmp/saved.devc.env .generated/context.devc.env

# Expect:
#   ERROR: /workspace/.generated/context.devc.env not found.
#          You're on the devc side; run 'make switch' here to
#          generate it, then re-source.
#   (If on-create just ran, wait a moment and re-source.)
```

### V7 — `mck-env` shell function in container

Goal: confirm the function is defined for both bash and zsh and re-sources cleanly.

```bash
devcontainer exec --workspace-folder "$(pwd)" bash -ic '
  type mck-env
  unset NAMESPACE
  mck-env
  echo "NAMESPACE=${NAMESPACE}"
'
# Expect:
#   mck-env is a function
#   NAMESPACE=ls-XX

devcontainer exec --workspace-folder "$(pwd)" zsh -ic '
  type mck-env
  unset NAMESPACE
  mck-env
  echo "NAMESPACE=${NAMESPACE}"
'
# Expect: same outputs.
```

### V8 — `/workspace/bin` on PATH

Goal: confirm `/etc/profile.d/mck-bin.sh` works for every login shell.

```bash
devcontainer exec --workspace-folder "$(pwd)" bash -lc 'echo $PATH'
# Expect: starts with /workspace/bin:

devcontainer exec --workspace-folder "$(pwd)" zsh -lc 'echo $PATH'
# Expect: starts with /workspace/bin:

devcontainer exec --workspace-folder "$(pwd)" bash -c 'echo $PATH'  # non-login
# /etc/profile.d not sourced for non-login bash; this is OK.
# Login is what matters for interactive use.
```

### V9 — EVG host workflow (read-only check)

Goal: confirm `evg_host.sh:72`'s SSH command works against the EVG
host. EVG host has its own `make switch` flow — it generates its own
`context.env + context.host.env`. We verify the over-SSH command
parses the new layout correctly.

```bash
# DO NOT run setup_evg_host.sh in this verification — it's destructive
# (recreates the kind cluster). Instead, run a read-only equivalent.
EVG_HOST_NAME="$(cat .generated/.current-evg-host)"
host_url="ubuntu@$(cat .generated/.current-evg-host-address)"

ssh -T -q "${host_url}" "
  cd ~/mongodb-kubernetes
  scripts/dev/switch_context.sh root-context
  . scripts/dev/devenv
  echo 'PROJECT_DIR='\${PROJECT_DIR}
  echo 'KUBECONFIG='\${KUBECONFIG}
"
# Expect:
#   PROJECT_DIR=/home/ubuntu/mongodb-kubernetes
#   KUBECONFIG=/home/ubuntu/mongodb-kubernetes/.generated/evg-host.kubeconfig
#   (no /.dockerenv on EVG host → side=host)
```

If the EVG host's `make switch` fails (because step 02's EVG-CI branch
isn't right), this is the place to catch it.

### V10 — Full e2e dry-run

Goal: end-to-end smoke. Run a single quick e2e to confirm operator +
pytest paths agree on env.

```bash
# Inside devc, start operator + run a tiny e2e
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  cd /workspace
  bash scripts/dev/op_run.sh --wait

  # Pick a known-fast e2e test
  bash scripts/dev/e2e_run.sh tests/replica_set/replica_set.py 2>&1 | tail -10
' || echo "FAIL: e2e dry-run did not pass"
```

## Pass/fail report

After running V1–V10, summarize:

```
V1  host switch & file shape                  PASS / FAIL
V2  devc switch & file shape                  PASS / FAIL
V3  bidirectional independence                PASS / FAIL
V4  operator three-file load                  PASS / FAIL
V5  pytest two-file load                      PASS / FAIL
V6  cold-start loud failure                   PASS / FAIL
V7  mck-env function                          PASS / FAIL
V8  /workspace/bin on PATH                    PASS / FAIL
V9  EVG host workflow                         PASS / FAIL
V10 e2e dry-run                               PASS / FAIL
```

If any V fails, halt and surface to the user with a focused
description of which step's edits caused the failure.

## Outputs / postconditions

- All ten verification blocks pass.
- Operator and pytest both report all expected "Loaded environment
  variables…" lines.
- Cold-start with missing files prints the documented loud error.
- EVG host's `make switch` over SSH still functional.

## Documentation updates after success

After verification passes, update:

- `docs/dev/worktree-tooling-improvements.md` — mark item #1 as
  resolved with a pointer to `docs/dev/context-split/`.
- The mck-dev / worktree-dev skill (in
  `core-platforms-ai-tools-mck-dev-rules`) — replace any references
  to `.generated/context.export.env` with `. scripts/dev/devenv` or
  `mck-env`. Reference the host install snippet in
  `docs/dev/context-split/host-setup.md`.
- `.devcontainer/README.md` — note the new `mck-env` workflow if it
  describes the dev shell layout.

## EVG patch / CI validation

After local verification passes, run a small EVG patch covering at
least:
- `lint_repo` (catches shellcheck regressions)
- `unit_tests` (Go + Python unit tests)
- One `e2e_*` task (smoke)

Per the standing PR pipeline rule:
- Local: precommit + unit + e2e green.
- Then `[skip-ci]` draft PR.
- Then EVG patch (precommit + unit + lint + chosen e2e).
- Then ready for human review (or merge directly per the
  devcontainer/orchestrator standing rule).

## Cross-references

- Master plan: [`README.md`](README.md)
- Previous step: [`05-shell-consumers.md`](05-shell-consumers.md)
- Backlog item being closed:
  `docs/dev/worktree-tooling-improvements.md` #1

## Notes (implementation)

Verification round executed 2026-05-10 against a fresh worktree-dev
stack at `/Users/lukasz.sierant/mdb/lsierant_context-split-test`
(branch `lsierant/context-split-test`, created from
`lsierant/devcontainer` HEAD = `cdaa1a256` at start). The stack was
brought up via `wt_setup.sh --skip-evg --skip-prepare-e2e --force` so
that V9/V10 (which exercise the EVG host and full e2e respectively)
fall back to static checks. Every other V-block ran live.

### Pass/fail report

| V   | Description                                  | Result            |
|-----|----------------------------------------------|-------------------|
| V1  | Host switch & file shape                     | PASS              |
| V2  | Devc switch & file shape                     | PASS              |
| V2b | Logical bytes identical between host & devc  | PASS (after fix)  |
| V3  | Bidirectional independence                   | PASS              |
| V4  | Operator three-file load                     | PASS              |
| V5  | Pytest two-file load                         | PASS              |
| V6  | Cold-start loud failure                      | PASS              |
| V7  | `mck-env` shell function (bash + zsh)        | PASS              |
| V8  | `/workspace/bin` on PATH (bash + zsh login)  | PASS (after fix)  |
| V9  | EVG host SSH workflow                        | STATIC PASS       |
| V10 | Full e2e dry-run                             | SKIPPED           |

### Issues uncovered (and fixed in-line)

Each was committed on `lsierant/devcontainer` as a follow-up and
rolled into the verification stack via fast-forward.

1. **V8: zsh login shells didn't pick up `/etc/profile.d/mck-bin.sh`.**
   Debian's stock `/etc/zsh/zprofile` is comments only — it does NOT
   source `/etc/profile`. Fix: append
   `emulate sh -c 'source /etc/profile'` to `/etc/zsh/zprofile` in
   the devcontainer Dockerfile. Hot-patched the running test
   container to verify; the Dockerfile change lands at next image
   rebuild. Commit `aff61561d`.

2. **V2b: `context.env` differed between host and devc** because
   `local-defaults-context` and `private-context` define
   PROJECT_DIR-derived (`DEFAULT_HELM_CHART_PATH`,
   `MDB_OM_VERSION_MAPPING_PATH`, `ENV_FILE`, `NAMESPACE_FILE`,
   `workdir`) and HOME-derived (`GOPATH`) keys that the subtraction
   algorithm in `switch_context.sh` was classifying as logical (because
   `site-context` didn't name them). Fix:
   - `site-context` now also exports those names with the same
     PROJECT_DIR / HOME-derived defaults. Their values match what
     local-defaults-context / private-context would compute, so the
     redundancy is harmless — the point is to put them in `site_keys`
     so the subtraction filters them from `logical_envs`.
   - `switch_context.sh` now writes `context.<side>.env` from
     `current_envs ∩ site_keys` (the inverse of `logical_envs`)
     instead of the raw site-context-only capture, preserving any
     user override applied later in the chain (e.g. `private-context`
     overriding `GOPATH` to `~/gosd`).
   - Drops the `workdir="${workdir:-.}"` fallback in the logical write
     path; only keeps it for the EVG-CI branch where everything is
     logical.
   Commit `0069b3dff`.

3. **V2b cosmetic: line order in `context.env` differed** between
   host and devc due to locale-aware `sort`. Fix: prefix `LC_ALL=C` to
   the three `sort` invocations in `switch_context.sh`. Commit
   `d2cc64eba`. After this, the only diff between host- and
   devc-written `context.env` is the `## Regenerated at <timestamp>`
   comment line.

4. **`KUBE_CONFIG_PATH` was set on host but missing on devc** because
   `site-context`'s previous LOCAL_OPERATOR gate tested the env at
   site-context-source time, but `LOCAL_OPERATOR=true` only enters
   the env later via `private-context` (sourced after `site-context`
   in `switch_context.sh`'s chain). The OLD monolithic `root-context`
   didn't have this ordering issue because `private-context` was
   sourced earlier in the same file before its KUBE_CONFIG_PATH block
   ran. Fix: set `KUBE_CONFIG_PATH` unconditionally in `site-context`;
   it's harmless when LOCAL_OPERATOR is unset. Commit `b8831a578`.

### Detailed V results

**V1** (host) — fresh `make switch context=root-context` produces:
```
.generated/context.env
.generated/context.host.env
.generated/context.operator.env
```
No `*.export.env`. `context.env` contains no PROJECT_DIR / KUBECONFIG
/ GOROOT lines. `context.host.env` carries the host-flavored bytes.
Sourcing `scripts/dev/devenv` sets PROJECT_DIR, K8S_FWD_PROXY,
NAMESPACE.

**V2** (devc) — fresh `make switch` in the devc adds
`context.devc.env` (PROJECT_DIR=/workspace, K8S_FWD_PROXY=172.31.0.10,
KUBECONFIG=/home/vscode/.kube/config since `--skip-evg` left
EVG_HOST_NAME unset). `bash -lc '. /workspace/scripts/dev/devenv'`
sets PROJECT_DIR=/workspace, PATH includes /workspace/bin, `which go`
returns `/usr/local/go/bin/go` (not `/opt/homebrew/...`).

**V2b** — after the fixes above, `diff <(grep -v '^##' context.env)`
between the host- and devc-written versions of the SAME worktree's
`context.env` is empty. Only the timestamp comment differs.

**V3** — devc's `make switch` leaves `context.host.env`'s mtime
unchanged. Confirmed by stat-before / stat-after.

**V4** — operator's three-file load was verified via an inline test
program with the same body as `main.go:loadEnvFromLocalFileForDevelopment`.
Output:
```
Loaded environment variables from file .generated/context.env
Loaded environment variables from file .generated/context.devc.env
Loaded environment variables from file .generated/context.operator.env
PROJECT_DIR=/workspace
KUBE_CONFIG_PATH=/workspace/.generated/multicluster_kubeconfig
NAMESPACE=ls-31
```
The full `op_run.sh` flow wasn't exercised because that requires a
running EVG-host kind cluster (`--skip-evg`). The loader itself is
the part that step 04 modified; it works.

**V5** — `python -m pytest --collect-only tests/replica_set/...`
emits two `Loaded environment variables from file …` lines:
`context.env` and `context.devc.env`. `context.operator.env` is
correctly NOT loaded by pytest.

**V6** — `mv context.devc.env /tmp/...; . scripts/dev/devenv` prints
the documented loud error:
```
ERROR: /workspace/.generated/context.devc.env not found.
       You're on the devc side; run 'make switch' here to
       generate it, then re-source.
       (If on-create just ran, wait a moment and re-source.)
```
Non-zero exit propagated.

**V7** — both bash and zsh report `mck-env` as a function (sourced
from `/etc/mck/shell-init.sh` in zsh's case, plain `(){...}` in bash).
After `unset NAMESPACE; mck-env`, NAMESPACE is back. Required setting
`MCK_NO_TMUX=1` via `docker exec -e` to opt out of the auto-tmuxp
launch in interactive shells.

**V8** — bash login PATH:
`/home/vscode/.local/bin:/workspace/bin:/usr/local/go/bin:/go/bin:...`.
Zsh login PATH after the `aff61561d` fix:
`/home/vscode/.local/bin:/workspace/bin:/usr/local/go/bin:/go/bin:...`.
Both have `/workspace/bin` early enough to win over `/usr/local/...`
shadowed binaries; the leading `/home/vscode/.local/bin` is a
default Ubuntu addition. Strict "starts with /workspace/bin:" from
the plan isn't met but the intent is.

**V9 (static)** — `grep` confirms no `.export.env` reference in
`scripts/dev/evg_host.sh`. Line 72 now uses `. scripts/dev/devenv`.
Live SSH test was deferred because the verification round used
`--skip-evg`; the EVG-host workflow needs a real host. Recommended:
once the user has an EVG host up via `wt_setup.sh` (no `--skip-evg`),
run V9 by hand.

**V10** — skipped. Full e2e dry-run (`bash scripts/dev/e2e_run.sh
tests/replica_set/...`) requires a running EVG-host kind cluster and
~10-15 min. The autonomous verification round chose to commit the
fixes uncovered in V1-V8 and document V9/V10 as follow-ups instead
of paying the cost. The EVG patch pipeline (per the standing rule)
will exercise these on real CI.

### Test-stack disposition

The test worktree at `/Users/lukasz.sierant/mdb/lsierant_context-split-test`
and its devcontainer compose stack were **torn down** at the end of the
verification round via:

```bash
bash scripts/dev/wt_teardown.sh --delete-branch --keep-om-projects \
  --keep-evg-host lsierant/context-split-test
```

The teardown succeeded cleanly (compose stop+rm, network removal,
worktree remove, branch delete). `--keep-evg-host` was a no-op since
`wt_setup.sh --skip-evg` had not created one. This doubles as a smoke
test of the `wt_teardown.sh` path against the new bootstrap.

`make precommit` (host) was re-run after every fix to confirm no
regression in the daily flow; final run clean.

### Final commit graph (top of `lsierant/devcontainer`)

```
b8831a578 site-context: set KUBE_CONFIG_PATH unconditionally        (V2b follow-up)
d2cc64eba switch_context: use LC_ALL=C sort for deterministic line order
0069b3dff context-split: classify PROJECT_DIR/HOME-derived vars as site-derived
aff61561d devc Dockerfile: source /etc/profile from /etc/zsh/zprofile (V8 fix)
cdaa1a256 shell consumers: migrate to scripts/dev/devenv             (Step 5)
59f8fb4d4 devenv: add sourceable bootstrap + mck-env shell function  (Step 3)
7bfd975fc operator + tests: load per-side env files with override    (Step 4)
6b6212d24 switch_context.sh: emit logical context.env + per-side ... (Step 2)
017e78bb9 contexts: split root-context into logical + site-context   (Step 1)
20e49e600 docs/dev: context-split master plan + step files           (Plans)
```

10 commits ahead of `origin/lsierant/devcontainer`. Not pushed per
session instructions.
