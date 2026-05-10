# wt-ctl — unified worktree + devcontainer control plane

## Goal

Replace the patchwork of `wt_setup.sh`, `wt_teardown.sh`, `dc_attach.sh`,
`dc_build.sh`, `dc_up.sh`, and `dc_select_network.sh --list` with a single
maintainable Python tool, `scripts/dev/wt-ctl`, that:

- Always operates in the context of the **current worktree** (resolved from
  `cwd`); prints the worktree it's targeting on the first line so mistakes
  are caught early.
- Presents a high-level "do what I mean" verb set for humans **and**
  a complete primitive surface for agents and recovery.
- Offers a single, concise output format suitable for both humans and agents
  (no `--json`).
- Wraps the existing proven bash workhorses without rewriting them. Only
  orchestration logic moves to Python.

## Non-goals (for now)

- `wt-ctl source` (sourceable env). Dropped — devenv already does this and
  layered concerns (site-context + venv) make it premature.
- Native Python rewrite of `create_worktree.sh` (kept as a wrapper).
- Rewrite of `dc_select_network.sh` registry/locking logic (wrapped).
- Rewrite of `evg_prepare.sh`, `evg_host.sh`, `delete_om_projects.sh`,
  `.devcontainer/scripts/*` (all wrapped).
- A `kind` subcommand. Plain `kubectl get nodes` / `kind get clusters` are
  sufficient.
- A `--json` flag. The single output format is concise human-readable.

## Conventions

- **Tool name**: `wt-ctl`.
- **Binary**: `scripts/dev/wt-ctl` — a thin shim that `exec`s
  `python3 -m wt_ctl "$@"`.
- **Package**: `scripts/dev/wt_ctl/` (Python ≥3.11, **stdlib only**).
- **Cwd resolution**: every command resolves the *current* worktree from
  cwd by walking up to the nearest `.git` (file or dir) and confirming
  `git rev-parse --show-toplevel` is inside a worktree of the main repo.
  If cwd isn't inside any worktree:
  - For verbs that target the current worktree (status, attach, up, …) →
    error with a hint pointing at `wt-ctl create <branch>` or
    `wt-ctl status --all`.
  - For verbs that take an explicit branch (`status <branch>`,
    `delete <branch>`) → still allowed.
- **First-line banner** (always, on **stderr** so stdout stays parseable):
  ```
  [wt-ctl] worktree=<dir>  branch=<branch>
  ```
  Suppressed with `--quiet`.
- **Error model**: typed exceptions in `wt_ctl.errors`; `cli.py` translates
  to a stderr message + non-zero exit. No bare `except`. No
  `try/except: pass`. Domain modules **must not** import `subprocess`.
- **Exit codes**: 0 = ok, 1 = command failure, 2 = misuse / preconditions
  unmet (not in worktree, branch not found, etc.).
- **Logs**: phase logs land at `<worktree>/logs/setup_worktree/<phase>.log`
  (existing convention). `wt-ctl logs` tails them.

## Subcommand surface

```
# High-level
wt-ctl status [<branch>] [--all]
wt-ctl create <branch> [--context CTX] [--multi-cluster] [--force]
                       [--skip-evg] [--skip-devcontainer] [--skip-prepare-e2e]
                       [--resume] [--restart-from PHASE]
wt-ctl delete [<branch>] [--delete-branch] [--keep-evg] [--keep-worktree]
                          [--keep-stack] [--keep-om-projects]
wt-ctl up
wt-ctl down
wt-ctl build [--no-cache]
wt-ctl attach [args...]      # bare → tmux mck; with args → exec verbatim w/ MCK_NO_TMUX=1
wt-ctl logs [<phase>] [-f]   # phase: evg|build|up|refresh|prepare-e2e|create|delete

# Primitives
wt-ctl network  list | prune [--dry-run] | release [<branch>] | allocate [<branch>]
wt-ctl evg      status | prepare [--multi] [--skip-recreate] | terminate
                | extend [--hours N] | kubeconfig
wt-ctl om       list | clean
wt-ctl ctx      show | switch <ctx>
wt-ctl wt       list | new <branch> [--force] | rm [<branch>]
```

## Output format

Single concise format, key-value aligned. ANSI colors **off** by default;
auto-enabled only when stdout is a tty (`--color=always|never|auto`,
default auto). Banner always to stderr.

### `wt-ctl status` (current worktree)

```
[wt-ctl] worktree=lsierant_devcontainer  branch=lsierant/devcontainer

worktree    lsierant_devcontainer  (clean, 0 ahead/0 behind master)
path        /Users/lukasz.sierant/mdb/lsierant_devcontainer
context     e2e_smoke

devc        running    project=lsierant_devcontainer_devcontainer  image_age=2h
network     172.28.0.0/16    prefix=28    registered=2026-05-08    orphans=0
namespace   mck-devc-28-mongodb-test
gost-proxy  http://127.0.0.1:8028

evg host    running    name=lsierant_devcontainer  id=i-0abc1234  expires_in=3h12m
ssh         ssh ec2-user@ec2-44-200-12-34.compute-1.amazonaws.com
kubeconfig  fresh    last_patch=12m_ago    kfp_registered=yes

om          2 projects in scope `lsierant_devcontainer-mck_devc_28-*`

next        wt-ctl attach          # tmux mck
            wt-ctl attach zsh      # bare shell
```

### `wt-ctl status --all`

Replaces `dc_select_network.sh --list`. Columns include EVG host name,
namespace, devc state, expiry. Trailing block proposes cleanup commands
for prunable worktrees and orphan registrations.

```
WORKTREE                BRANCH                  NET  NAMESPACE                    DEVC      EVG-NAME              EXPIRES
lsierant_devcontainer   lsierant/devcontainer   28   mck-devc-28-mongodb-test     running   lsierant_devcontainer 3h12m
search_lb-improvements  search/lb-improvements  30   mck-devc-30-mongodb-test     exited    -                     -
context-split-review    lsierant/ctx-split      31   mck-devc-31-mongodb-test     -         lsierant_ctx-split    terminating

prunable
  search_lb-improvements   reason=devc-exited  →  wt-ctl delete search/lb-improvements
  context-split-review     reason=evg-terminating → wt-ctl delete lsierant/ctx-split

orphan registrations
  oldworktree-host-kfp     prefix=26  →  wt-ctl network release oldworktree-host-kfp
```

Pruning rules (initial heuristic):
- A worktree is **prunable** if its devc stack is exited *and* it has no
  EVG host running; or its branch has been merged/deleted upstream; or
  its registry entry has no matching worktree directory.
- Orphan registrations are entries from `dc_select_network.sh --list`
  whose `branch_dir` has no corresponding worktree on disk.

The status code for `status --all` is always 0 (informational); prunable
worktrees are surfaces, not failures.

## Layout

```
scripts/dev/wt-ctl                       # thin shim: exec python3 -m wt_ctl "$@"
scripts/dev/wt_ctl/
    __init__.py
    __main__.py                          # parses args, dispatches
    cli.py                               # argparse subparsers — no subprocess
    runner.py                            # the only file that imports subprocess
    errors.py                            # WtCtlError hierarchy
    paths.py                             # worktree resolution, log dir helpers
    header.py                            # first-line banner to stderr
    display.py                           # status / status --all renderers
    state.py                             # State dataclasses (WorktreeState, NetEntry, …)
    domains/
        __init__.py
        worktree.py                      # git worktree state, branch info
        compose.py                       # docker compose ps / down / project name
        devcontainer.py                  # devcontainer up / build / exec
        network.py                       # wraps dc_select_network.sh
        evg.py                           # wraps evg_host.sh + evergreen CLI
        omprojects.py                    # wraps delete_om_projects.sh
        contextsw.py                     # make switch + .generated/.current_context
        kubeconfig.py                    # parse + summarize ~/.kube/config age
```

## `runner.py` contract

The single subprocess façade. Domain modules receive a `Runner` instance;
they NEVER import `subprocess`. Linted via a custom check in
`scripts/test/wt_ctl_lint.py`.

```python
from dataclasses import dataclass, field
from pathlib import Path

@dataclass
class CmdResult:
    argv: list[str]
    rc: int
    stdout: str
    stderr: str
    duration_s: float

class Runner:
    def run(self, argv: list[str], *, check: bool = True,
            capture: bool = True, env: dict | None = None,
            cwd: Path | None = None, timeout: float | None = None,
            input_text: str | None = None) -> CmdResult: ...

    def run_streaming(self, argv: list[str], *, prefix: str,
                      log_path: Path, env: dict | None = None,
                      cwd: Path | None = None) -> CmdResult: ...

    def run_parallel(self, jobs: list[Job]) -> dict[str, CmdResult]: ...
        # all complete; failures aggregated and raised together as
        # ParallelPhaseFailures.
```

Errors: `ToolMissing`, `NotInWorktree`, `BranchNotFound`,
`ExternalCommandFailed(argv, rc, stdout, stderr)`,
`ParallelPhaseFailures(failures: dict[str, ExternalCommandFailed])`,
`StateConflict`, `RegistryError`. All inherit `WtCtlError`. `cli.py` is
the only place that catches `WtCtlError`.

Lint rule: `scripts/test/wt_ctl_lint.py` greps `domains/*.py` for
`import subprocess` / `from subprocess`; CI failure if found.

## Phasing

### Phase 1 — scaffold + read + simple write verbs (no orchestrator yet)

Deliverables:
1. Package layout above.
2. `runner.py`, `errors.py`, `paths.py`, `header.py`, `display.py`,
   `state.py`, `cli.py`, `__main__.py`.
3. Domain modules: `worktree`, `compose`, `devcontainer`, `network`,
   `evg`, `omprojects`, `contextsw`, `kubeconfig`.
4. Subcommands implemented:
   - `status`, `status --all`
   - `attach` (bare = tmux; args = exec verbatim with `MCK_NO_TMUX=1`)
   - `up`, `down`, `build`
   - `network list/prune/release/allocate` (all → `dc_select_network.sh`)
   - `evg status/extend/terminate/kubeconfig`
   - `om list/clean`
   - `ctx show/switch`
   - `wt list/new/rm` (wraps `create_worktree.sh` + `git worktree`)
   - `logs <phase> [-f]`
5. Shims for previous user-facing entry points so existing skills/scripts
   don't break:
   - `scripts/dev/dc_attach.sh` → `exec scripts/dev/wt-ctl attach "$@"`
   - `scripts/dev/dc_build.sh`  → `exec scripts/dev/wt-ctl build "$@"`
   - `scripts/dev/dc_up.sh`     → `exec scripts/dev/wt-ctl up "$@"`
   - Each shim prints a one-line deprecation note to stderr.
6. Unit tests in `scripts/test/wt_ctl/`:
   - `test_runner.py` — `Runner.run` happy path + failure mapping using a
     fake `subprocess` injected via `Runner` constructor.
   - `test_paths.py` — cwd resolution from inside / outside a worktree.
   - `test_display.py` — renders fixture state to expected text.
   - `test_lint.py` — runs the import-subprocess checker.
7. Smoke test (`scripts/test/wt_ctl_smoke.sh`) that runs
   `wt-ctl status` from this worktree and asserts the banner + at
   least these keys: `worktree`, `path`, `network`, `gost-proxy`.
8. `scripts/test/wt_ctl_lint.py` enforces the no-subprocess-in-domains
   rule.

Acceptance:
- `wt-ctl status` in this worktree shows correct devc / network / evg /
  om / namespace / gost-proxy URL.
- `wt-ctl status --all` lists all worktrees, marks prunable ones, and
  proposes commands.
- `wt-ctl attach`, `wt-ctl attach zsh`, `wt-ctl attach make help` all
  behave like the current `dc_attach.sh`.
- `wt-ctl up` is idempotent (re-uses running container).
- `wt-ctl build`, `wt-ctl down`, `wt-ctl logs build`, `wt-ctl network
  list` all work.
- Unit tests pass; smoke test passes.

### Phase 2 — orchestration with resume

Deliverables:
1. `wt-ctl create <branch> [...]` — full pipeline:
   ```
   worktree_init    → create_worktree.sh (or skip if already on it)
   initialize_hook  → .devcontainer/scripts/initialize.sh
   net_allocate     → dc_select_network.sh --branch-dir
   parallel:
     evg_prepare    → evg_prepare.sh
     dc_build       → devcontainer build
   dc_up            → devcontainer up
   reconcile        → compose up -d --force-recreate --no-deps for compose.user.yml services
   post_start       → .devcontainer/scripts/post-start.sh
   kubeconfig       → in-container `make switch` + `evg_host.sh get-kubeconfig --no-fetch`
   prepare_e2e      → in-container `make prepare-local-e2e`
   ```
2. State file `<worktree>/.wt-ctl/state.json` tracking each phase
   (`pending` / `in_progress` / `ok` / `failed`), with `input_hash`
   (sha256 of the inputs that affect the phase: context, multi-cluster,
   skip-flags, prefix). `--resume` skips `ok` phases whose hash matches
   current inputs; `--restart-from <phase>` clears state from that phase
   forward.
3. `wt-ctl delete <branch>` — full teardown matching `wt_teardown.sh`:
   om clean → compose down → evg terminate → worktree remove → prefix
   release → optional branch delete. Each step best-effort; failure of
   one doesn't abort the rest. No state file (delete is one-shot
   forward-only).
4. `status` learns to surface "last create: failed at <phase>" when
   state file shows a failed phase, with the resume hint inline.
5. Shims for `wt_setup.sh` / `wt_teardown.sh` exec `wt-ctl create` /
   `wt-ctl delete`. Each prints a deprecation note.

Acceptance:
- `wt-ctl create <fresh_branch>` runs the full pipeline against a real
  EVG host and reaches green status.
- Killing the script mid-`evg_prepare` and re-running with `--resume`
  picks up where it left off; `--restart-from net_allocate` re-runs
  prefix allocation.
- `wt-ctl delete <branch>` cleans up everything and `wt-ctl status
  --all` no longer lists the worktree or its registry entry.

### Phase 3 — end-to-end validation on real EVG host + search e2e

1. Use `wt-ctl create lsierant/wtctl-e2e` (a fresh disposable branch from
   the current worktree's HEAD) to bring up a brand-new worktree, EVG
   host, and devcontainer.
2. After `wt-ctl status` reports green, exec into the container and:
   - Build & deploy the operator (`make operator`, `make deploy`).
   - Run a single search e2e test (e.g. `make e2e
     test=e2e_search_community_basic` or whichever the repo's golden-path
     search e2e is — pick the shortest passing one).
3. Confirm test passes; record the command + log path.
4. Run `wt-ctl delete lsierant/wtctl-e2e` to clean up.
5. Final `wt-ctl status --all` should not list the test worktree or its
   prefix entry.

## Key design decisions (locked in for autonomous execution)

| Decision | Choice | Reason |
|---|---|---|
| Language | Python ≥3.11 stdlib only | already used in repo for tests; iteration speed; easy subprocess + dataclass story; no toolchain to install |
| State file location | `<worktree>/.wt-ctl/state.json` | separates state from logs; easy to gitignore |
| Output format | Single concise human-readable; ANSI auto | per user feedback (no --json) |
| Banner stream | stderr | keeps stdout parseable for piping |
| Workhorses | preserved as bash | proven, locking + complex side effects already tested |
| Subprocess discipline | only `runner.py` imports subprocess; lint enforces | maintainability ask from user |
| Error handling | typed exceptions; cli.py is sole catch site; no broad except | maintainability ask from user |
| Tests | stdlib `unittest` (or pytest if present) + smoke shell | no extra deps |
| Removal of old shims | not yet — keep through Phase 3 | safe transition for skills + agents |

## Testing on real infrastructure

- Phase 1 testing happens against the **current** worktree
  (`lsierant_devcontainer`). No new EVG hosts created.
- Phase 2 testing creates a fresh disposable worktree
  (`lsierant/wtctl-e2e`) on a fresh EVG host, then deletes it. Network
  prefix is allocated automatically (auto-prune may release stale
  entries from previous sessions — that's intended).
- Phase 3 runs a search e2e test inside that container and confirms
  pass/fail before cleanup.

## Out of scope for this branch

- 255+ prefix expansion (parked task #16): the registry rewrite is
  cleaner once `wt-ctl network` is the only entry point.
- Native Python `worktree.py` replacing `create_worktree.sh`.
- Replacing `evg_prepare.sh` internals.
- Removing the deprecation shims.
