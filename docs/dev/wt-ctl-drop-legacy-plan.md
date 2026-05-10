# wt-ctl: drop legacy /16 prefix support

## Goal

Remove all "legacy" code paths from `wt-ctl`'s network registry — the
ones that recognise `MCK_DEVC_NET_PREFIX=NN` (NN in 16..31) as a full
`172.NN.0.0/16` occupation. The user has authorised tearing down all
previously-created devcontainer stacks; after this cleanup the only
supported scheme is the /23-per-stack indexing introduced in `786672072`
(2048 stacks within `172.16.0.0/12`).

Net effect: ~150 lines deleted, no behavioural change for new-scheme
stacks, simpler mental model.

## Migration steps the user runs *first* (before the cleanup commit lands)

```sh
# 1. Tear down every existing devcontainer stack (legacy and new alike).
#    For each branch with a stack — including the currently-running host
#    worktree (lsierant/devcontainer) if you're willing to recreate it:
wt-ctl delete <branch>     # or `--keep-worktree` to keep the dir
                            # (skip --delete-branch unless you also want
                            # to nuke the git branch)

# 2. Wipe the registry to be safe (idempotent — no-op if already empty):
rm -f ~/.cache/mck-devc/net-prefix-registry
rm -rf ~/.cache/mck-devc/net-prefix-registry.lock.d

# 3. Apply the cleanup commit (this plan).

# 4. Recreate stacks under the new scheme:
wt-ctl create <branch>
```

If the user wants to **keep** the host worktree (`lsierant_devcontainer`)
running rather than delete + recreate:

```sh
# Before the cleanup commit, migrate its .env so compose.yml has the
# four new vars — already supported by `wt-ctl network migrate-env`.
cd /Users/lukasz.sierant/mdb/lsierant_devcontainer
wt-ctl network migrate-env

# Then drop its registry row (it's a legacy bare-int line):
wt-ctl network release lsierant_devcontainer

# Re-allocate a fresh new-scheme index for it. The next `wt-ctl up`
# would otherwise default compose's MCK_DEVC_NET_X to the fallback (28)
# and break the running stack.
wt-ctl create lsierant/devcontainer --resume --restart-from net_allocate
# ↑ the orchestrator's net_allocate will see a populated .env and skip;
#   alternative: manually delete the 5 vars from .devcontainer/.env first
#   so net_allocate runs fresh.
```

Verify cleanup with:
```sh
wt-ctl network list           # should show only new-scheme entries
                              # (no scheme=legacy column)
```

## Code changes (the cleanup commit itself)

### `scripts/dev/wt_ctl/domains/network.py`

Drop:
- `LEGACY_X_LO`, `LEGACY_X_HI` constants.
- `StackParams.scheme` field; collapse `subnet`, `ip_range`,
  `proxy_address` to a single branch (the current `scheme=='new'` body).
  `vip_cidr` is already scheme-agnostic.
- `stack_params(index, *, scheme="legacy")` branch; signature becomes
  `stack_params(index)` — only the new path. Bounds check stays
  `INDEX_LO..INDEX_HI`.
- The bare-int registry-row parser. `_RegistryRow.from_line` only
  parses `<branch_dir>=<N>:<X>:<Y_BASE>:<Y_VIP>:<PORT>`. A line that
  fails to parse should warn and be dropped (don't silently fall back
  to legacy).
- The `if row.params.scheme == "legacy":` arm in `Registry.allocate()`.
  Now the only blockers are `used_indices` (other registry rows) and
  `blocked_xs` from `_docker_networks_in_range()` (kind, custom docker
  networks, etc.). The "block raw legacy index value to keep namespace
  suffix unique" fix from `90370510b` becomes unnecessary — drop it,
  since legacy entries no longer exist.
- The legacy-display branch in `render_list()` — the entire "scheme"
  column in the extension table.
- Module docstring legacy section (lines 37..62-ish) — keep just the
  /23 math.
- `migrate_legacy_env()` and the `wt-ctl network migrate-env` CLI
  subcommand can stay as a one-shot upgrade tool, OR be removed if the
  user is committed to the full delete-and-recreate flow. Recommend
  **remove** for full simplicity (the user said "delete all previously
  created devcontainers"). If we remove, also drop `sk_me` parser,
  `cmd_network`'s `migrate-env` branch, and the corresponding test.

### `scripts/dev/wt_ctl/cli.py`

- `subnet_for(prefix)` and `gost_proxy_for(prefix)` (used in status
  display): drop the legacy-band check that returns `172.NN.0.0/16` for
  16..31. Single branch using `stack_params(prefix).subnet` /
  `.gost_proxy`.
- If keeping `migrate-env`: leave it. If dropping: remove `sk_me` lines
  in `build_parser` + the `if sub == "migrate-env"` branch in
  `cmd_network`.

### `.devcontainer/compose.yml`

- Single set of fallback defaults for the four new vars. Pick anything
  sensible — e.g. all defaults map to stack index 0:
  `${MCK_DEVC_NET_X:-16}.${MCK_DEVC_NET_Y_BASE:-0}` and
  `${MCK_DEVC_NET_Y_VIP:-1}` and `${MCK_DEVC_PROXY_PORT:-8000}`. Drop
  `MCK_DEVC_NET_PREFIX:-28` chains — `MCK_DEVC_NET_PREFIX` is still
  written by the allocator as the index (used by `root-context` to
  build NAMESPACE), but compose.yml itself no longer reads it for any
  IP construction.

### `scripts/dev/contexts/root-context`

- The `NAMESPACE=<base>-${MCK_DEVC_NET_PREFIX}` suffix logic is
  unaffected — `MCK_DEVC_NET_PREFIX` still holds the stack index
  (0..2047), still works as a unique suffix. No change required.

### Tests

- `scripts/test/wt_ctl/test_network_registry.py`:
  - Drop tests prefixed/named for legacy: `test_blocked_x_for_legacy_*`,
    `test_legacy_*`, `test_mixed_legacy_new_round_trip`,
    `test_legacy_index_reuse_blocked` (the one added by `90370510b`).
  - Keep round-trip, locking, stale-lock recovery, orphan detection,
    auto-prune-on-allocate, math (boundary indices 0/127/128/1023/2047).
  - Update fixture seeders that emit `branch_dir=NN` lines to emit the
    composite form.
- `scripts/test/wt_ctl/test_network_list_format.py`:
  - Drop the legacy fixture parity tests. The bash-output byte-equal
    guarantee is no longer load-bearing; the bash script is gone.
  - Optionally: capture a fresh fixture from `wt-ctl --quiet network list`
    after the cleanup and assert byte-stable output across future
    refactors.
- `scripts/test/wt_ctl/test_orchestrator.py`:
  - The 5-line env block check stays (orchestrator writes all five vars
    on net_allocate; that's unchanged).

### `docs/dev/wt-ctl-plan.md`

- Update Phase 2.5 / Phase 4 sections to note the legacy removal as a
  separate phase (Phase 5 — cleanup of /16 vestiges).
- This file (`wt-ctl-drop-legacy-plan.md`) supersedes itself once the
  work is committed; can be deleted in the same commit.

## Acceptance

- `python3 -m unittest discover -s scripts/test/wt_ctl -t scripts/test/wt_ctl` — all green.
- `python3 scripts/test/wt_ctl_lint.py` — clean.
- `bash scripts/test/wt_ctl_smoke.sh` — pass.
- `wt-ctl --quiet network list` — table shows only `BRANCH_DIR / N / X /
  Y_BASE / Y_VIP / PORT`. No `scheme` column. Pruning hints reference
  `wt-ctl network …` (already true since `e7e007264`).
- `wt-ctl network allocate <fake>` then `wt-ctl network release <fake>`
  — round-trip clean against an empty registry.
- `wt-ctl create <fresh-branch>` (in a test session) — runs the full
  pipeline; `state.json` reaches all `ok`; `.devcontainer/.env` has
  all five `MCK_DEVC_NET_*` vars; compose stack comes up.
- `git grep -in legacy scripts/dev/wt_ctl/ scripts/test/wt_ctl/` — only
  hits should be in this plan file or short comments explaining the
  one-shot migration history (e.g. "v0.x supported a /16 scheme;
  removed in commit <hash>"). Zero behavioural references.

## Commit policy

- Single commit titled `wt-ctl: network — drop legacy /16 scheme`.
  Larger than usual but it's a coherent deletion; splitting risks
  intermediate broken states.
- Body: explicit migration steps (this file's "Migration steps"
  section), commit hash that introduced the /23 scheme (`786672072`),
  links to the deleted symbols.
- DO NOT push, DO NOT create JIRA tickets, DO NOT open PRs.

## Out of scope (don't do here)

- Re-tightening the index space (e.g. dropping unused 0..15 because
  there are no legacy entries). Keep the full 0..2047 range —
  predictability over micro-optimisation.
- Renaming `MCK_DEVC_NET_PREFIX` to `MCK_DEVC_STACK_INDEX` or similar.
  Backward semantic name; nothing breaks; renames invalidate any
  external grep.
- Changing the registry on-disk lock primitive from mkdir to flock.
- Touching kind cluster / metallb / 172.18 hardcoding in tests.
