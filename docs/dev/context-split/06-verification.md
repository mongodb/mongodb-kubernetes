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
