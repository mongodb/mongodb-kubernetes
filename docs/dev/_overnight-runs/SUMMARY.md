# Overnight run summary — 2026-05-10 → 2026-05-11

## High-level outcome

Five phases executed. Phases 1–3 completed cleanly. Phases 4 and 5
(the wet e2e tests) ran far enough to **surface and fix 5 real
Phase-1 regressions in `wt-ctl evg spawn` and `wt-ctl net_allocate`**
plus a Phase-2 cleanup gap, but **neither phase reached actual e2e
test execution** — both were blocked on the same legacy docker
network issue on the EVG host (a sibling `lsierant_devcontainer-test`
worktree's stack claimed `172.16.0.0/16`, blocking every `/23`
allocation inside the supernet).

## What landed on `lsierant/devcontainer`

After Phase 1's single commit (`35d8797d6`), the wet tests piled on
five more fixes:

| SHA | Subject | Source |
|---|---|---|
| `35d8797d6` | wt-ctl: native EVG spawn — drop mck-dev dep | Phase 1 plan |
| `69f79d701` | wt-ctl: evg spawn — parse plain-text 'keys list', prefer evg-host | Phase 5 wet test |
| `f84bacfbd` | wt-ctl: evg spawn — drop reserved tag-key, surface keys-list diag | Phase 4 wet test |
| `61024b669` | wt-ctl: evg spawn — resolve placeholder via pre-create snapshot diff | Phase 5 wet test |
| `10b001a44` | wt-ctl: net_allocate — repair partial .devcontainer/.env on resume | Phase 4 wet test |
| `51cd6f3b2` | wt-ctl: net_allocate — invalidate input_hash when .env is partial | Phase 5 wet test |

Stack rebased twice (Phase 2 retry, then again after each Phase 4/5 fix).
Current state: `search/lb-improvements → KUBE-17 → KUBE-26 → KUBE-27`
all sit on top of `51cd6f3b2`.

## Per-phase outcome

### Phase 1 — native EVG spawn (✅ complete)
Single commit `35d8797d6`. 77 unit tests green. Report:
`phase1-20260511T002000Z.md`. Plan file deleted in-commit.

### Phase 2 — cascade rebase (✅ complete after retry)
First attempt blocked on `scripts/dev/wt_setup.sh` (deleted on devc tip,
modified by `97dda3a85` + `4d770a396` on lb-improvements + `ba5e10b20`
on KUBE-26). Resolved surgically (reset+cherry-pick, dropping the
3 dead-file commits and the `5b80385e5` carry-over that was already
upstream). All four worktrees `go build` clean. Reports:
`phase2-20260510T221934Z.md`, `phase2-retry-20260510T222348Z.md`.

### Phase 3 — mck-dev skill update (✅ complete)
`local-kind-dev/SKILL.md` got a prominent "When to use wt-ctl (MCK repo
dev)" section + verb table + cross-link to the on-host kfp daemon.
`evergreen-api/SKILL.md` got a one-line MCK pointer under `spawn_host`.
Plus a **12-entry UX-gaps table** (most flag `help=` strings on
create/delete missing; `branch` vs `branch_dir` mismatch in
`network release/allocate`; hardcoded 172.[16-31] range in
`network prune --networks`). Report: `phase3-20260510T221425Z.md`.

### Phase 4 — KUBE-26 wet e2e (🟡 partial)
Time-boxed out before reaching the in-devc operator + e2e. Surfaced
two Phase-1 regressions, fixed and cascaded:

- `f84bacfbd` — `evg spawn` used reserved AWS tag key `name=<n>` → HTTP
  400, display-name never landed, resume probes treated every spawn as
  "not-found" and duplicated hosts.
- `10b001a44` — `_do_net_allocate` short-circuited when `.devcontainer/.env`
  had only `MCK_DEVC_NET_PREFIX` and none of the 4 derived vars; compose
  fell back to defaults and every parallel worktree raced on
  `172.16.0.0/23`.

Pre-Phase-1 issue unfixed: a legacy `lsierant_devcontainer-test_…`
docker network on the EVG host claims `172.16.0.0/16`. Worked around
in this run by reallocating to prefix 1280 (X=26). Report:
`phase4-20260510T230228Z.md`.

### Phase 5 — KUBE-27 wet e2e (🟡 partial)
Same blocker as Phase 4 (legacy `/16` on host). Found three more
Phase-1 regressions, fixed and cascaded:

- `69f79d701` — `_resolve_key_name` called `evergreen keys list --json`
  (no such flag). Switched to plain-text parse; prefer canonical
  `evg-host`.
- `61024b669` — Poll loop couldn't track `evg-*` → `i-*` id transition
  with a sibling spawn racing; added pre-create `--mine` snapshot +
  set-diff in the poll loop.
- `51cd6f3b2` — `net_allocate.input_hash` short-circuited on resume even
  when `.env` was partially written, so the sibling Phase 4 repair never
  fired. Hash now includes the missing-derived-keys set.

85 unit tests green. Report: `phase5-20260510T230441Z.md`.

## Most important UX issues discovered (cross-phase)

These are the recurring ones that hit both subagents. The full per-phase
lists are in each phase report.

1. **`branch` positional inconsistency.** `wt-ctl create` and
   `wt-ctl delete` require an explicit positional `branch`, but
   `status`, `build`, `up`, `down` infer it from cwd. Treating these
   verbs symmetrically (infer from cwd when omitted) is the obvious
   fix.
2. **Empty `--help` bodies.** `wt-ctl attach`, `wt-ctl kfp`,
   `wt-ctl network`, `wt-ctl om`, `wt-ctl ctx`, `wt-ctl wt` and many
   sub-verbs have placeholder help. Phase 3 logged 12 specific gaps.
3. **Orchestrator swallows devcontainer-cli stderr.** When `dc_up`
   fails, the user gets an opaque return code but no actionable
   diagnostic. Logs end up half-empty. Phase 4 spent significant
   time chasing this.
4. **Pre-Phase-1 legacy /16 docker network detection.** When any
   docker network on the EVG host owns a `/16` inside `172.16.0.0/12`,
   wt-ctl should detect at `net_allocate` time and refuse instead of
   letting compose fail downstream with "overlapping IPv4". Both
   wet tests hit this from different angles.
5. **Parallel-phase fail-fast violation.** `evg_prepare` and `dc_build`
   run in parallel; if one fails, the other is allowed to keep going
   and the eventual error report is misleading. Violates
   `feedback_fail_fast_no_masking.md`.
6. **`last_create failed at <X>` shows wrong phase on SIGTERM.** When
   the orchestrator is killed mid-flight, the resume marker points
   at whatever phase happened to be last-touched, not the actually-
   in-progress one.
7. **`evg_prepare` has no sub-checkpoints.** A resume after a kind
   recreate failure re-runs the full kind teardown+recreate, even if
   the host SSH + kubeconfig steps were already done.
8. **`wt-ctl status` "not-found" false positive.** Hosts with a null
   `display_name` (e.g., the pre-`f84bacfbd` hosts in our pool right
   now) match by `instance_tags` but render as `not-found`. Confusing
   while debugging.

## What's still broken / needs human attention

1. **Three orphan EVG hosts** with null display names (artifacts of
   the pre-`f84bacfbd` bug):
   - `i-0e401a17e7fbffd27`  (from before the overnight run)
   - `i-0e339dd72c2e106ec`  (Phase 4)
   - `i-067399fa25956abe2`  (Phase 5)

   Auto-mode classifier blocked sweep-termination from the leader
   session; please run:
   ```sh
   for h in i-0e401a17e7fbffd27 i-0e339dd72c2e106ec i-067399fa25956abe2; do
     evergreen host terminate --host "$h"
   done
   ```

2. **`lsierant_devcontainer-test_…` legacy /16 docker network on
   EVG host**. Until cleaned up, every wt-ctl create on hosts inside
   `172.16.0.0/12` risks the same blocker. SSH into the host and:
   ```sh
   docker network ls | grep 172.16
   docker network rm <name>
   ```
   Or delete the stale `lsierant_devcontainer-test` worktree entirely.

3. **Section A + B never actually ran** for either KUBE-26 or
   KUBE-27. The fixes that landed are tested only at the unit-test
   layer (85 tests green); they have **not** been validated end-to-end
   against a live host yet. Recommend a fresh wet test on KUBE-26
   once the docker-network cleanup is done.

4. **Phase 4 / Phase 5 worktrees + their kind clusters** were left
   running (Section C cleanup not reached). `wt-ctl delete` from each
   worktree will tear them down, but only after the three orphan
   hosts above get explicit display names from a manual modify, OR
   the user accepts a "host not found by name" warning and lets
   `wt-ctl delete` proceed for everything else.

## Suggested next action

Once you have a coffee:

1. Sweep the 3 orphan hosts (one shell command above).
2. SSH into a fresh EVG host and remove any `172.16.0.0/16` docker
   networks owned by stale worktree stacks.
3. Run a single fresh `wt-ctl create` from one of the KUBE-* worktrees
   to validate the 5 Phase-1 fixes end-to-end against the actual e2e
   suite.
4. Triage the UX issues table from `phase3-*.md` — most are
   1–2-line `help=` strings, low-effort polish.

## Files

- Reports: `_overnight-runs/phase{1..5}-*.md`
- Pre-rebase tip captures: `_overnight-runs/phase2-{pre-rebase,retry-pre}-tips.txt`
- Create-flow logs: `_overnight-runs/phase{4,5}-create*.log`
- This summary: `_overnight-runs/SUMMARY.md`
