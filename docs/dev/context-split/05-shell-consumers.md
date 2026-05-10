# Step 05 — Migrate shell consumers to the new bootstrap

## Goal

Replace every direct `source .generated/context.export.env` (or
`.export.env` variants) with the new `scripts/dev/devenv` bootstrap or
the rewritten `scripts/dev/set_env_context.sh` helper. Drop the
`## Side:` fail-fast guard and the `${PROJECT_DIR}/bin` PATH prepend
from `set_env_context.sh`. After this step, no script references
`.export.env` files anywhere.

## Preconditions

- Steps 01–04 complete (or at least merged together; this step is the
  final consumer migration).
- `scripts/dev/devenv` exists (step 03).
- `.generated/context.{env,host.env,devc.env,operator.env}` are
  produced by step 02's `switch_context.sh`.

This step lands the breaking change: until it's merged with steps 01–04,
the consumers below still reference `.export.env`, which step 02 stops
generating. **Do not push step 02 alone.** Sequence the commits as a
short stack on `lsierant/devcontainer` and push only when this step
lands.

## Files touched

- `scripts/dev/set_env_context.sh` (rewrite as a thin wrapper)
- `scripts/dev/op_run.sh` (lines around 30-31 and 75)
- `scripts/dev/e2e_run.sh` (line 28)
- `scripts/dev/evg_host.sh` (line 72; runs on EVG host)
- `scripts/dev/create_worktree.sh` (line 75)
- `scripts/dev/delete_om_projects.sh` (verify usage; may be
  comment-only)
- `.devcontainer/scripts/post-start.sh` (comment-only today; verify)
- `.devcontainer/scripts/on-create/01-switch-context.sh` (comment-only
  today; verify)
- `.devcontainer/scripts/pane_runner.sh` (comment-only today; verify)
- `.devcontainer/tmuxp/mck.yaml` (comment-only today; verify)

Plus removal of references to `.export.env` in any other places that
grep finds after the substantive consumers are migrated.

## Detailed plan

### 1. Rewrite `scripts/dev/set_env_context.sh`

Currently (verbatim):

```bash
#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

# shellcheck disable=1091
source scripts/funcs/errors

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")
context_file="$(realpath "${script_dir}/../../.generated/context.export.env")"

if [[ ! -f ${context_file} ]]; then
    fatal "File ${context_file} not found! Make sure to follow this guide…"
fi

# Side guard: refuse to source a context.export.env that was generated on the
# OTHER side …
side_stamp_file="${context_file%.export.env}.env"
if [[ -f "${side_stamp_file}" ]]; then
    file_side="$(grep -m1 '^## Side: ' "${side_stamp_file}" | awk '{print $3}' || true)"
    current_side="$([[ -f /.dockerenv ]] && echo container || echo host)"
    if [[ -n "${file_side}" && "${file_side}" != "${current_side}" ]]; then
        fatal "${side_stamp_file} was generated on side='${file_side}' but you're sourcing it on side='${current_side}'."
    fi
fi

source "${context_file}"

export PATH="${PROJECT_DIR}/bin:${PATH}"
```

Replace with:

```bash
#!/usr/bin/env bash
#
# Sourceable helper for scripts that want the per-side env loaded
# without re-implementing the bootstrap. Prefer sourcing
# scripts/dev/devenv directly in new code; this file exists for
# backward compatibility with scripts that historically sourced
# .generated/context.export.env via this helper.
#
# Behavior:
#   - Resolves the worktree root via the script's own location.
#   - Sources scripts/dev/devenv (which fails loudly if env files
#     are missing, picks the right side via /.dockerenv, sources
#     logical + site with `set -a`, and activates venv if present).
#   - Does NOT prepend ${PROJECT_DIR}/bin to PATH — that's handled
#     in the container by /etc/profile.d/mck-bin.sh, and on the host
#     by the dev's own ~/.zshrc.
#
# See docs/dev/context-split/README.md for the full design.

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

_set_env_context_script="$(readlink -f "${BASH_SOURCE[0]}")"
_set_env_context_dir="$(dirname "${_set_env_context_script}")"
_set_env_context_root="$(realpath "${_set_env_context_dir}/../..")"

# shellcheck disable=SC1091
. "${_set_env_context_root}/scripts/dev/devenv"

unset _set_env_context_script _set_env_context_dir _set_env_context_root
```

Notes:
- The wrapper IS sourced (callers do `source scripts/dev/set_env_context.sh`).
  `set -Eeou pipefail` at the top changes the calling shell's settings.
  This matches today's behavior; document if it surprises a script.
- `.` is POSIX-portable equivalent of `source`; both work in bash.
- The `unset` at the end keeps namespace clean.
- The `## Side:` stamp check is gone — the new design makes
  side-mismatch impossible by name.
- The `${PROJECT_DIR}/bin` prepend is gone — replaced by
  `/etc/profile.d/mck-bin.sh` (container) or dev rc (host).

### 2. `scripts/dev/op_run.sh` — lines around 29-32 and 75

Existing (lines 29-32):

```bash
set -a
source .generated/context.export.env
source .generated/context.operator.export.env
set +a
```

Replace with:

```bash
# Source the per-side env via the canonical bootstrap. devenv handles:
#   - side detection via /.dockerenv
#   - logical .generated/context.env + site .generated/context.<side>.env
#   - venv activation if present
# Operator-specific overlay (.generated/context.operator.env) is loaded
# by main.go's loadEnvFromLocalFileForDevelopment, not by this shell.
# shellcheck disable=SC1091
. scripts/dev/devenv
```

Existing (line 75) — the inner tmux command:

```bash
tmux new-session -d -s mck-operator \
  "bash -lc 'set -a; source .generated/context.export.env; source .generated/context.operator.export.env; set +a; cd \"$(pwd)\"; ${proxy_env} go run ./main.go ${operator_args} 2>&1 | tee ${log_path}'"
```

Replace with:

```bash
tmux new-session -d -s mck-operator \
  "bash -lc 'cd \"$(pwd)\"; . scripts/dev/devenv; ${proxy_env} go run ./main.go ${operator_args} 2>&1 | tee ${log_path}'"
```

Notes:
- `bash -lc` runs a login shell, which sources `/etc/profile.d/mck-bin.sh`
  (container) — gets `/workspace/bin` on PATH.
- `. scripts/dev/devenv` then sets logical + site env.
- main.go (after step 04) loads `context.operator.env` itself; no
  need to source it here.

### 3. `scripts/dev/e2e_run.sh` — line 28

Existing (lines 26-29):

```bash
# shellcheck disable=SC1091
set -a
source .generated/context.export.env
set +a
```

Replace with:

```bash
# shellcheck disable=SC1091
. scripts/dev/devenv
```

`devenv` handles `set -a` itself, so the explicit wrapper is redundant.

### 4. `scripts/dev/evg_host.sh` — line 72 (runs on EVG host)

Existing:

```bash
ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; source .generated/context.export.env; scripts/dev/setup_evg_host.sh ${auto_recreate}"
```

Replace with:

```bash
ssh -T -q "${host_url}" "cd ~/mongodb-kubernetes; scripts/dev/switch_context.sh root-context; . scripts/dev/devenv; scripts/dev/setup_evg_host.sh ${auto_recreate}"
```

The EVG host has no `/.dockerenv` → `devenv` resolves side=host and
sources `context.env + context.host.env`. EVG host's own `make switch`
(via `switch_context.sh root-context`) populates these files on the
EVG host's filesystem.

### 5. `scripts/dev/create_worktree.sh` — line 75

Existing:

```bash
source .generated/context.export.env
```

Replace with:

```bash
. scripts/dev/devenv
```

This script runs on host during worktree bootstrap. Side resolves to host.

### 6. Audit comment-only references

Run a grep and confirm these are all comment-only or string literals
in messages (no actual sourcing):

```bash
cd /workspace
grep -rn 'context\.export\.env\|context\.operator\.export\.env' \
    scripts/ .devcontainer/ Makefile docker/ \
    --exclude-dir=node_modules --exclude='*.md' 2>/dev/null
```

Expected after step 5 substantive edits:
- Comment lines explaining historical reasons (rewrite to reference
  the new file names).
- Error/help message strings (rewrite to reference `make switch` and
  `devenv`).

Update each:
- `scripts/dev/switch_context.sh:87` (comment) — update wording.
- `scripts/dev/delete_om_projects.sh:21` (comment) — update wording.
- `.devcontainer/tmuxp/mck.yaml:57` (comment) — update wording.
- `.devcontainer/scripts/pane_runner.sh:26` (comment) — update wording.
- `.devcontainer/scripts/on-create/01-switch-context.sh:3` (comment)
  — update wording.

After this round of edits:

```bash
grep -rn 'context\.export\.env' scripts/ .devcontainer/ docker/ 2>/dev/null
# Expect: zero matches.
```

### 7. `.devcontainer/scripts/post-start.sh`

Already updated by commit `77e708164` to re-PATCH the kubeconfig on every
container start. It does NOT source the context env file directly —
verify by reading the current state. If it sources, swap to `devenv`.
If not, leave alone.

### 8. Verify EVG CI path still works

`switch_context.sh` has the `EVR_TASK_ID` branch (CI). After steps
01-04, that branch reads `${context_file}`, sources it, and writes
`.generated/context.env`. The CI invocations don't go through this
step's consumer migrations (they run on EVG runners with their own
flow). Smoke-test by hand-running:

```bash
EVR_TASK_ID=fake make switch context=root-context
ls .generated/context*.env
# Expect: context.env (logical) + context.host.env (CI runner is "host")
#         + context.operator.env. No errors.
```

If the CI runner's `make switch` fails with the new code, fix
`switch_context.sh`'s EVG branch in this step (or step 02).

## Verification

```bash
# Sanity grep
grep -rn 'context\.export\.env' /workspace 2>/dev/null
# Expect: 0 matches

# Full op_run smoke test (assumes operator can build + run locally)
cd /workspace
bash scripts/dev/op_run.sh --wait
tmux capture-pane -t mck-operator -p | head -50
# Expect: "Loaded environment variables from file .generated/context.env"
#         "Loaded environment variables from file .generated/context.devc.env"
#         "Loaded environment variables from file .generated/context.operator.env"
#         operator starts without "command not found"

tmux kill-session -t mck-operator

# E2E smoke
bash scripts/dev/e2e_run.sh -m smoke 2>&1 | tail -20
# Expect: pytest collection includes the test set; env vars present.
# Don't need to run the full suite here; collection-level success is enough.

# Set_env_context.sh used by older scripts
bash -c 'source scripts/dev/set_env_context.sh && echo "NAMESPACE=${NAMESPACE}" && echo "PATH=${PATH}"'
# Expect: NAMESPACE set; PATH unchanged from the calling shell (no
# /workspace/bin: prepend, since that comes from profile.d in interactive
# shells).
```

## Outputs / postconditions

- No script in the repo references `.generated/context.export.env` or
  `.generated/context.operator.export.env`.
- All shell consumers go through `scripts/dev/devenv` directly (or
  via `set_env_context.sh`).
- `set_env_context.sh` is now a thin wrapper around `devenv`; no
  side-stamp check; no PATH manipulation.
- `op_run.sh`, `e2e_run.sh`, `evg_host.sh`, `create_worktree.sh` all
  use the bootstrap.

## Pitfalls

- **`bash -lc` vs. `bash -c`** in `op_run.sh:75`. The `-l` flag matters
  — it triggers `/etc/profile.d/*.sh` sourcing, which puts
  `/workspace/bin` on PATH (via `mck-bin.sh` from step 03). Without
  `-l`, the tmux subshell's PATH wouldn't include `/workspace/bin`.
  Keep the `-l`.
- **Re-sourcing in tmux pane**. The `bash -lc '. scripts/dev/devenv; ...'`
  form runs `devenv` which calls `set -a; source ...; set +a`. The
  exported vars persist in the bash subshell that runs the operator.
  Confirmed by today's `set -a; source ...; set +a` pattern in
  op_run.sh — same effect.
- **Side-stamp file `.generated/context.env`'s `## Side: <side>` line**.
  After step 02, the new files include a `## Side: <side>` comment for
  human-discoverability (it's just a comment, not parsed by anything).
  No script reads it. Don't reintroduce a fail-fast based on it.
- **`scripts/dev/delete_om_projects.sh:21`** today reads NAMESPACE from
  context.env. Verify the new env loading still gives it the right
  NAMESPACE. NAMESPACE is logical → in `context.env` → bind-mount-shared
  → both sides see the same value. ✓
- **`set_env_context.sh` callers expecting PATH prepend**. Audit one
  more time. Today the `export PATH="${PROJECT_DIR}/bin:${PATH}"` at
  the end of set_env_context.sh ensures /workspace/bin is on PATH for
  any script that sources it. After this step's rewrite, the prepend
  is gone. In container: profile.d covers it. On host: dev's
  responsibility. For one-off scripts that don't run via login shell,
  manually `export PATH="${PROJECT_DIR}/bin:${PATH}"` if needed.
  Document in the commit message.
- **EVG host script (`setup_evg_host.sh`)** sourced after `devenv` in
  the new `evg_host.sh:72` line. Verify it doesn't re-export PATH or
  do anything that depends on `/.dockerenv` (EVG host has no
  `/.dockerenv` → side=host, correct). If it has a host PATH-prepend
  expectation, add a one-line `export PATH="${PROJECT_DIR}/bin:${PATH}"`
  to setup_evg_host.sh's top, since EVG host doesn't have a profile.d
  drop-in installed.

## Commit

Suggested as a single commit at the end of this step (or split across
two: one for `set_env_context.sh` rewrite, one for consumer migration —
either is fine).

```
shell consumers: migrate to scripts/dev/devenv

Replace every `source .generated/context.export.env` (and
`.operator.export.env`) with `. scripts/dev/devenv` (or via
set_env_context.sh which now wraps devenv). Touched:

  - scripts/dev/op_run.sh (top-level + tmux subshell)
  - scripts/dev/e2e_run.sh
  - scripts/dev/evg_host.sh (over-SSH command on EVG host)
  - scripts/dev/create_worktree.sh
  - scripts/dev/set_env_context.sh (rewrite as thin wrapper)
  - comment-only references in switch_context.sh, delete_om_projects.sh,
    .devcontainer/{tmuxp/mck.yaml,scripts/pane_runner.sh,
    scripts/on-create/01-switch-context.sh}

Drops the ## Side: fail-fast in set_env_context.sh (impossible by
construction with per-side filenames) and the ${PROJECT_DIR}/bin PATH
prepend (replaced by /etc/profile.d/mck-bin.sh in the devc image,
managed by the dev on host).

Step 5/6 of docs/dev/context-split.
```

## Cross-references

- Master plan: [`README.md`](README.md)
- Previous steps: [`03-devenv-bootstrap.md`](03-devenv-bootstrap.md),
  [`04-godotenv-consumers.md`](04-godotenv-consumers.md)
- Final step: [`06-verification.md`](06-verification.md)

## Notes (implementation)

Implemented on `lsierant/devcontainer` as a single commit migrating every
shell consumer of the old `.export.env` files to the new `devenv`
bootstrap, and rewriting `set_env_context.sh` as a thin wrapper.

### Files changed

- `scripts/dev/set_env_context.sh` — full rewrite per plan section 1.
  Now `unset`s its private vars (`_set_env_context_*`) at the bottom
  and delegates to `${root}/scripts/dev/devenv` via `.`. Side-stamp
  guard and `${PROJECT_DIR}/bin` PATH prepend both removed.
- `scripts/dev/op_run.sh` — both occurrences updated. Top-level (lines
  ~26-32) replaced with `. scripts/dev/devenv`. Inner tmux command
  (line ~75) now runs `bash -lc 'cd …; . scripts/dev/devenv; … go
  run ./main.go …'`. The `-l` flag preserved so
  `/etc/profile.d/mck-bin.sh` puts `/workspace/bin` on PATH.
- `scripts/dev/e2e_run.sh` — `set -a; source …; set +a` block
  replaced with `. scripts/dev/devenv`. The explicit
  `source venv/bin/activate` block kept for its loud error path
  (`venv` missing → fail with a useful message); `devenv` activates
  idempotently so the duplication is harmless.
- `scripts/dev/evg_host.sh` — line 72 SSH command updated. EVG host's
  remote shell now runs `. scripts/dev/devenv` after
  `switch_context.sh root-context`. Side resolves to host (no
  `/.dockerenv`).
- `scripts/dev/create_worktree.sh` — line 75 `source .generated/…`
  replaced with `. scripts/dev/devenv`. The explicit
  `source venv/bin/activate` on line 74 kept for clarity (devenv
  activates again, idempotent).

### Comment-only / message-string updates

- `scripts/dev/switch_context.sh` — the `rm -f *.export.env` cleanup
  comment now says "All consumers now go through scripts/dev/devenv".
- `scripts/dev/delete_om_projects.sh:21` — comment switched from
  `context.export.env` to `context.env`.
- `.devcontainer/tmuxp/mck.yaml:57` — comment now describes
  `mck-env`/`devenv` loading `context.env` + `context.devc.env`.
- `.devcontainer/scripts/pane_runner.sh:26` — same wording update.
- `.devcontainer/scripts/on-create/01-switch-context.sh` — header
  comment now references the new file pair (`context.env` +
  `context.devc.env`) and `devenv`.
- `.devcontainer/shell-init.sh` — header comment updated to describe
  the new env-loading shape (already invokes `devenv` at line 24,
  per step 03).
- `Makefile` — comment above `regenerate-context` updated; the old
  side-stamp guard explanation is gone (no guard exists anymore),
  replaced with a description of why the OTHER side's site bytes
  don't clobber this side's (separate per-side filenames).
- `scripts/test/bash/run.sh:57` — `[[ -f .generated/context.export.env
  ]]` precondition flipped to `[[ -f .generated/context.env ]]`.
  Functional change (the file the script tests for exists), not just
  a comment.
- `scripts/test/bash/worktree/test_om_project_namespace.bats` — the
  comment-only reference to `context.export.env` updated.

### Deviations from the plan

1. **`scripts/test/bash/run.sh` was missing from the plan's checklist
   but had a functional reference.** Found it via the audit grep
   (line 57: `[[ -f .generated/context.export.env … ]]`). Updated to
   `context.env`. Without this, bats subshells stop sourcing
   `set_env_context.sh` after step 02 stops emitting the old file —
   not catastrophic (the conditional silently no-ops), but it would
   leave the original "fail-with-unbound-variable" issue in place.

2. **`.devcontainer/shell-init.sh` had a comment-only reference**
   that wasn't in the plan's section-6 list. Updated for consistency
   with the rest of the migrated files.

3. **`Makefile` comment above `regenerate-context`** explicitly
   referenced the side-stamp guard. After this step the guard is
   gone, so I rewrote the comment to describe the new rationale
   (separate per-side filenames make side mismatch impossible by
   construction). The plan listed Makefile as touched only via
   "lines 42, 45, 394 — they source set_env_context.sh"; it didn't
   call out the comment block at lines 53-58. Caught by the audit
   grep.

4. **`setup_evg_host.sh` left untouched.** Plan's pitfalls section
   said: "if it has a host PATH-prepend expectation, add a one-line
   `export PATH="${PROJECT_DIR}/bin:${PATH}"`." Read the file end-to-end:
   downloads `kind`, `kubectl`, `helm` to `/usr/local/bin` and uses
   only system binaries (`curl`, `sudo`, `tee`, `tar`, etc.). No
   `${PROJECT_DIR}/bin/` reference. No PATH manipulation needed. EVG
   host has no `/etc/profile.d/mck-bin.sh` but doesn't need one for
   this script's purpose.

5. **`.devcontainer/scripts/post-start.sh` left untouched** per the
   plan's section 7 (verified — it does not source the context env;
   it only PATCHes a kubeconfig to the in-container k8s-proxy).

### Stubs removed

```
.generated/context.export.env
.generated/context.operator.export.env
```

Both were re-emitted as workarounds during steps 02, 03, and 04 so
`pre-commit` could run. After step 05 they are no longer needed and
are gone. Confirmed `make switch` does NOT regenerate them (the
`rm -f` line at the end of `switch_context.sh` keeps them out).

### Verification snippets

Audit grep — zero matches:

```
$ grep -rn 'context\.export\.env' scripts/ .devcontainer/ docker/ Makefile 2>/dev/null
(empty)
```

`set_env_context.sh` sources cleanly with no stubs:

```
$ rm -f .generated/context.export.env .generated/context.operator.export.env
$ bash -c 'source scripts/dev/set_env_context.sh && \
    echo "NAMESPACE=${NAMESPACE}" && echo "PROJECT_DIR=${PROJECT_DIR}"'
NAMESPACE=ls
PROJECT_DIR=/Users/.../lsierant_devcontainer
```

`make precommit` — clean run, no `.export.env` errors:

```
$ make precommit 2>&1 | tail -5
isort....................................................................Passed
ShellCheck v0.10.0.......................................................Passed
ty.......................................................................Passed
govulncheck..............................................................Passed
golangci-lint............................................................Passed
```

(The earlier-step Notes' "temporary stub" workaround is now obsolete;
pre-commit no longer needs the stub `.export.env` files.)

`bash -n` on every touched script: all parse.

EVG-CI smoke (`EVR_TASK_ID=fake make switch context=root-context`):
exits clean, writes the same three files, no `.export.env` artifacts.

### Issues uncovered for step 06

- **`scripts/evergreen/*` and many other scripts source
  `set_env_context.sh`.** All of them now flow through `devenv`. No
  visible breakage in pre-commit, but step 06 should exercise an EVG
  patch (precommit + unit + at least one e2e variant) to confirm the
  Evergreen runner side works end-to-end.

- **`bash -lc` vs. `bash -c` in op_run.sh's tmux command.** Kept the
  `-l` per the plan's pitfall note. Without `-l`, the tmux pane's PATH
  wouldn't include `/workspace/bin` (the new
  `/etc/profile.d/mck-bin.sh` only fires for login shells). End-to-end
  smoke (operator startup) belongs to step 06.

- **`devenv` activates the venv** as a side effect. Two consumers
  (`e2e_run.sh`, `create_worktree.sh`) still have an explicit
  `source venv/bin/activate` after the `devenv` source. It's
  idempotent — running it twice is a no-op — but a future pass could
  drop the explicit line and rely on `devenv` alone. Out of scope for
  step 05; flagged for cleanup in step 06's verification.
