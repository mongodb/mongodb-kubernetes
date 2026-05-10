# Step 03 — `devenv` bootstrap, `mck-env` shell function, PATH plumbing

## Goal

Provide a single, side-aware sourceable bootstrap (`scripts/dev/devenv`)
that loads logical + per-side context with `set -a` and activates the
project venv. Provide a `mck-env` shell function for ergonomics. Move
the `${PROJECT_DIR}/bin` PATH prepend out of `set_env_context.sh` and
into the devcontainer image's `/etc/profile.d/`.

## Preconditions

- Step 02 complete: `.generated/context.env` and
  `.generated/context.<side>.env` are produced by `make switch`.
- `.generated/context.operator.env` exists and is logical-only.

This step touches only NEW files and the Dockerfile. It does NOT yet
migrate consumers — that happens in step 05.

## Files touched

- `scripts/dev/devenv` (new file, sourceable)
- `.devcontainer/Dockerfile` (add `/etc/profile.d/mck-bin.sh`)
- `.devcontainer/shell-init.sh` (replace existing context source with
  `. /workspace/scripts/dev/devenv`; add `mck-env` function definition)
- `docs/dev/context-split/host-setup.md` (new: dev-facing host install
  doc) OR `.devcontainer/README.md` (append a section)

## Detailed plan

### 1. New `scripts/dev/devenv` (sourceable)

```bash
# scripts/dev/devenv — sourceable bootstrap for the per-side env.
#
# Usage (interactive shell):
#   . scripts/dev/devenv
# Or via the mck-env shell function (see shell-init.sh / your ~/.zshrc):
#   mck-env
#
# Loads .generated/context.env (logical, side-agnostic) and
# .generated/context.<side>.env (site, current side only) with allexport
# (`set -a`) so every KEY=VALUE is exported into the calling shell.
# Activates the venv if present.
#
# Fails loudly if either file is missing — first thing devs should do
# on a fresh worktree is `make switch`. After switch, re-source.
#
# This file is sourceable, not executable. Do not add a shebang or
# `set -Eeou` — it would change behavior in the calling shell.

# Resolve worktree root via PWD or git, so this script works whether the
# caller cd'd into the worktree or not.
__mck_root="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
if [[ ! -d "${__mck_root}/.generated" ]]; then
    echo "ERROR: .generated/ not found relative to $(pwd)." >&2
    echo "       Are you in a worktree? Run 'cd <worktree>' first." >&2
    unset __mck_root
    return 1 2>/dev/null || exit 1
fi

__mck_side=$([[ -f /.dockerenv ]] && echo devc || echo host)

if [[ ! -f "${__mck_root}/.generated/context.env" ]]; then
    echo "ERROR: ${__mck_root}/.generated/context.env not found." >&2
    echo "       Run 'make switch' first to generate it, then re-source." >&2
    unset __mck_root __mck_side
    return 1 2>/dev/null || exit 1
fi

if [[ ! -f "${__mck_root}/.generated/context.${__mck_side}.env" ]]; then
    echo "ERROR: ${__mck_root}/.generated/context.${__mck_side}.env not found." >&2
    echo "       You're on the ${__mck_side} side; run 'make switch' here to" >&2
    echo "       generate it, then re-source." >&2
    if [[ "${__mck_side}" == "devc" ]]; then
        echo "       (If on-create just ran, wait a moment and re-source.)" >&2
    fi
    unset __mck_root __mck_side
    return 1 2>/dev/null || exit 1
fi

set -a
# shellcheck disable=SC1090,SC1091
. "${__mck_root}/.generated/context.env"
# shellcheck disable=SC1090,SC1091
. "${__mck_root}/.generated/context.${__mck_side}.env"
set +a

if [[ -f "${__mck_root}/venv/bin/activate" ]]; then
    # shellcheck disable=SC1091
    . "${__mck_root}/venv/bin/activate"
fi

unset __mck_root __mck_side
```

Notes:
- No shebang. The file is sourced.
- `return 1 2>/dev/null || exit 1` works whether sourced or executed.
  Sourced: `return 1` works, `exit 1` would kill the calling shell.
- `unset __mck_root __mck_side` keeps the calling shell's namespace
  clean.
- Variables prefixed `__mck_` to avoid collision with anything the
  user's shell may define.

Set executable bit (`chmod 644` since it's sourced, not executed).

### 2. `.devcontainer/Dockerfile` — `/etc/profile.d/mck-bin.sh`

Add the PATH prepend as a profile drop-in. After the existing zsh /
shell-init COPY block (around current line 41), insert:

```dockerfile
# Add /workspace/bin (per-worktree project binaries: kubectl, helm, etc.
# pinned via Makefile install targets) to PATH for every login shell.
# This replaces the PATH prepend that lived in scripts/dev/set_env_context.sh
# in the old design. Keep it container-only — host devs add the
# equivalent to their ~/.zshrc themselves. See docs/dev/context-split/.
RUN printf 'export PATH="/workspace/bin:${PATH}"\n' \
        > /etc/profile.d/mck-bin.sh \
 && chmod 0644 /etc/profile.d/mck-bin.sh
```

Verify: every login shell in the container sees `/workspace/bin:` at
the front of PATH. Both bash (`-l`) and zsh source `/etc/profile.d/*.sh`
(zsh via `/etc/zprofile` -> `emulate sh -c '. /etc/profile'`; the
ubuntu base image already wires this).

### 3. `.devcontainer/shell-init.sh` — replace context source with devenv

Existing body:
```bash
if [[ $- == *i* ]]; then
    cd /workspace 2>/dev/null || true
    if [[ -f /workspace/.generated/context.export.env ]]; then
        source /workspace/.generated/context.export.env
    fi
    if [[ -f /workspace/venv/bin/activate ]]; then
        source /workspace/venv/bin/activate
    fi
fi
```

Replace with:

```bash
if [[ $- == *i* ]]; then
    cd /workspace 2>/dev/null || true
    # Source the per-side env (logical + site) and activate venv via the
    # canonical bootstrap. devenv detects /.dockerenv and picks
    # context.devc.env automatically. If files are missing (on-create
    # not finished, or fresh worktree without make switch), devenv
    # prints a loud warning and the rest of shell-init still runs.
    if [[ -f /workspace/scripts/dev/devenv ]]; then
        # shellcheck disable=SC1091
        . /workspace/scripts/dev/devenv || true
    fi
fi

# mck-env: ergonomic re-source after `make switch`. Defined for every
# shell, interactive or not, so scripts and dev shells share the same
# entry point. Fails non-zero if files are missing — propagates to caller.
mck-env() {
    # shellcheck disable=SC1091
    . /workspace/scripts/dev/devenv
}
```

The trailing `|| true` on the interactive bootstrap means an empty
worktree (no `make switch` yet) doesn't break the shell entirely —
the warning is printed but the user gets a usable prompt. Manual
`mck-env` after `make switch` then loads the env.

The tmux exec block at the bottom of shell-init.sh is unchanged.

### 4. Host install docs

Create `docs/dev/context-split/host-setup.md`:

```markdown
# Host shell setup for the MCK dev env

Add this one-line function to your `~/.zshrc` (or `~/.bashrc`):

    mck-env() { . "$(git rev-parse --show-toplevel 2>/dev/null || echo .)/scripts/dev/devenv"; }

Then in any worktree:

    cd <worktree>
    mck-env

`mck-env` sources `.generated/context.env` (logical) and
`.generated/context.host.env` (site bytes for the host) with
`set -a`, then activates the worktree's `venv/` if present.

If `.generated/context.host.env` is missing, run `make switch` first.

Optional: prepend the worktree's `bin/` to PATH so project-installed
`kubectl`, `helm`, etc. take precedence. The devcontainer does this
via `/etc/profile.d/mck-bin.sh`; on the host you control your own PATH.
A common pattern (zsh):

    chpwd() {
      [[ -d ./bin ]] && export PATH="$(realpath ./bin):${PATH}"
    }

…or just add the absolute path of the active worktree to PATH manually.
```

### 5. Pre-commit / shellcheck

Run `pre-commit run --all-files` after the Dockerfile + shell-init +
new files land. ShellCheck may complain about the SC1091 / SC1090
sourced-without-following-line; the inline directives in the snippets
above silence them.

## Verification

```bash
# Rebuild the devcontainer image (incremental — only the layer with
# /etc/profile.d/mck-bin.sh and the shell-init COPY changes).
bash scripts/dev/dc_build.sh --workspace-folder "$(pwd)"

# Restart the container.
bash scripts/dev/dc_up.sh --workspace-folder "$(pwd)"

# Inside the container, verify PATH and env load.
devcontainer exec --workspace-folder "$(pwd)" bash -lc '
  echo "PATH=${PATH}"
  echo "NAMESPACE=${NAMESPACE}"  # should be set from context.env
  which go && go version
  which kubectl
'
# Expect:
#   PATH starts with /workspace/bin:
#   NAMESPACE is set
#   /usr/local/go/bin/go (or wherever container go lives)
#   /workspace/bin/kubectl (if `make pre-commit` has been run)

# Verify mck-env function works after a manual unset.
devcontainer exec --workspace-folder "$(pwd)" bash -ic '
  unset NAMESPACE
  mck-env
  echo "NAMESPACE=${NAMESPACE}"
'
# Expect: NAMESPACE re-set.

# Verify devenv loud-fail when context files are missing.
mv .generated/context.devc.env /tmp/saved.context.devc.env
devcontainer exec --workspace-folder "$(pwd)" bash -lc '. /workspace/scripts/dev/devenv' 2>&1 | head -5
mv /tmp/saved.context.devc.env .generated/context.devc.env
# Expect: ERROR line about context.devc.env missing.
```

## Outputs / postconditions

- `scripts/dev/devenv` exists, executable bit unset, sourced cleanly.
- `.devcontainer/Dockerfile` adds `/etc/profile.d/mck-bin.sh`.
- `.devcontainer/shell-init.sh` sources `devenv` and defines `mck-env`.
- `docs/dev/context-split/host-setup.md` documents the host install.
- Inside the container, `/workspace/bin:` is the first entry in PATH for
  every login shell.
- Inside the container, every interactive shell has the env loaded
  automatically; manual refresh is `mck-env`.

## Pitfalls

- **Image rebuild required.** The `/etc/profile.d/mck-bin.sh` change
  is at image-build layer; existing containers don't pick it up until
  rebuild. Document in the commit.
- **Order in `/etc/profile.d/`**. Files are sourced alphabetically.
  `mck-bin.sh` placed AFTER the system's existing PATH-setters means
  our prepend wins. If the base image has a `zz-mck-bin.sh` style
  trick already, name accordingly.
- **`set -a` can leak vars to subprocesses.** That's the point — but
  if the user has weird vars in their shell (e.g., `_` or `OLDPWD`
  references), `set -a` auto-exports them too. Wrap the `. devenv`
  call in a subshell? No — that defeats the purpose. Accept the leak;
  it's the standard pattern.
- **`mck-env` defined in shell-init.sh works for both bash and zsh.**
  The `function name() { ... }` syntax is portable in both. The
  POSIX `name() { ... }` works too.
- **EVG host has no `shell-init.sh`.** EVG's `setup_evg_host.sh` runs
  before any user shell; devs who SSH in get plain bash without the
  function. They source `. scripts/dev/devenv` manually. Document in
  the EVG host SSH workflow if relevant.

## Commit

```
devenv: add sourceable bootstrap + mck-env shell function

Add scripts/dev/devenv: a sourceable file that loads
.generated/context.env (logical) and .generated/context.<side>.env
(site, picked via /.dockerenv) with `set -a`, then activates the venv.
Fails loudly with a clear message if either file is missing.

Add `mck-env` shell function in .devcontainer/shell-init.sh wrapping
`. /workspace/scripts/dev/devenv` for ergonomic re-source after
`make switch`.

Add /etc/profile.d/mck-bin.sh in the devcontainer Dockerfile to put
/workspace/bin on PATH for every login shell. Replaces the PATH
prepend that previously lived in set_env_context.sh.

Document host install in docs/dev/context-split/host-setup.md.

Step 3/6 of docs/dev/context-split.
```

## Cross-references

- Master plan: [`README.md`](README.md)
- Previous step: [`02-switch-context.md`](02-switch-context.md)
- Parallelisable with: [`04-godotenv-consumers.md`](04-godotenv-consumers.md)
- Next step: [`05-shell-consumers.md`](05-shell-consumers.md) depends
  on this step (consumers will source `devenv`).

## Notes (implementation)

Implemented on `lsierant/devcontainer` as a single commit. No image
rebuild in this step (verification skipped per the step's own guidance —
step 06 covers the live container test).

### What changed

- **`scripts/dev/devenv`** (new, mode 0644, sourceable, no shebang).
  Body matches the plan verbatim except for one addition: a
  `# shellcheck disable=SC2317` directive near the top of the file
  (after the file-level header comment, before `__mck_root=`). Without
  it, ShellCheck v0.10.0 flags the three
  `return 1 2>/dev/null || exit 1` lines as SC2317 (unreachable). The
  dual-form is intentional — sourceable scripts that may also be
  exec'd — and the disable is the documented escape hatch.
- **`.devcontainer/Dockerfile`** — added a new `RUN` block immediately
  after the `COPY shell-init.sh … && tee … .bashrc /home/vscode/.zshrc`
  block (i.e. after the last existing `RUN` for shell-init wiring,
  before the `mkdir /workspace/.generated` `RUN`). Block writes
  `/etc/profile.d/mck-bin.sh` with `export PATH="/workspace/bin:${PATH}"`,
  mode 0644. Verbatim from plan section 2.
- **`.devcontainer/shell-init.sh`** — replaced the `if [[ -f
  /workspace/.generated/context.export.env ]]; then source …; fi` /
  `venv/bin/activate` block with a single `. /workspace/scripts/dev/devenv
  || true` (devenv handles the venv activation itself). Added the
  `mck-env` shell function definition outside the `$- == *i*` guard
  so non-interactive scripts that source shell-init.sh also get the
  function. The trailing tmux exec block is unchanged.
- **`docs/dev/context-split/host-setup.md`** (new). Verbatim from
  plan section 4.

### Deviations from the plan

1. **`# shellcheck disable=SC2317` in `scripts/dev/devenv`.** Not in
   the plan; necessary to make pre-commit's ShellCheck hook pass on
   the `return 1 2>/dev/null || exit 1` dual-form. SC1090/SC1091 (the
   ones the plan does call out) are also disabled inline at the
   `source` lines — those came from the plan.

2. **No image rebuild done.** The plan's verification block walks
   through `dc_build.sh`/`dc_up.sh` and `devcontainer exec`. Per the
   step instructions in the kickoff message, the rebuild is deferred
   to step 06; this step verified script bodies via host-side syntax
   and source tests instead.

3. **Pre-commit `.export.env` stub workaround still required.**
   Step 02's notes flagged that `set_env_context.sh` consumers (used
   by pre-commit hooks `update-release-json`, `update-values-yaml`,
   `generate-standalone-yaml`, `generate-and-update`,
   `validate-helm-charts`) source `.generated/context.export.env`,
   which is no longer emitted. Same workaround applies here: I
   regenerated the stub from `context.env` + `context.host.env` (and
   `context.operator.export.env` from `context.operator.env`) before
   running pre-commit. Step 05 should remove the dependency entirely
   in `set_env_context.sh`.

### Issues uncovered for steps 05/06

- **`shell-init.sh` source path is hardcoded to `/workspace/`.** That's
  intentional — shell-init.sh is container-only — but it means the
  `mck-env` function defined there only works inside the devcontainer.
  Host devs install their own `mck-env` (one-liner pointed at
  `git rev-parse --show-toplevel`) per `host-setup.md`. Step 06's
  V1–V10 should exercise both paths; this step only verifies the
  container path's body parses.

- **`/etc/profile.d/mck-bin.sh` only takes effect on rebuild.** Step 02
  notes already flagged the consumer-breakage window between step 02
  and step 05. Step 03 doesn't change that window — `set_env_context.sh`
  still bridges to `.export.env` until step 05 lands. The new PATH
  prepend layer is dormant until the image is rebuilt; the existing
  `set_env_context.sh` PATH prepend keeps working in the meantime.

- **`mck-env` defined outside the `$- == *i*` guard.** The plan calls
  this out as a feature (scripts and dev shells share the entry
  point). One side effect: any `bash -c '. /etc/mck/shell-init.sh; …'`
  invocation now has `mck-env` available. No known caller does this,
  but if a future consumer relies on shell-init.sh being a no-op for
  non-interactive shells, the function definition leaks. Acceptable
  per plan.

### Verification snippets

`bash -n scripts/dev/devenv` and `bash -n .devcontainer/shell-init.sh`
both pass.

`shellcheck scripts/dev/devenv` and `shellcheck .devcontainer/shell-init.sh`
both pass after the SC2317 directive was added.

Sourcing on host succeeds:

```
$ bash -c '. scripts/dev/devenv && echo "PROJECT_DIR=${PROJECT_DIR}"'
PROJECT_DIR=/Users/.../lsierant_devcontainer
```

Loud-fail when `context.host.env` is missing:

```
$ mv .generated/context.host.env /tmp/saved && \
  bash -c '. scripts/dev/devenv'; echo "exit=$?"
ERROR: /Users/.../.generated/context.host.env not found.
       You're on the host side; run 'make switch' here to
       generate it, then re-source.
exit=1
```

`pre-commit run --files scripts/dev/devenv .devcontainer/Dockerfile
.devcontainer/shell-init.sh docs/dev/context-split/host-setup.md` —
all hooks pass (after re-emitting the temporary `.export.env` stubs
described above).

`hadolint` not on PATH; Dockerfile lint skipped per the step's own
guidance.
