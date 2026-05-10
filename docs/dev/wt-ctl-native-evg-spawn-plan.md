# wt-ctl: native EVG host spawn — drop mck-dev dependency

## Goal

Replace the two mck-dev CLI invocations in `scripts/dev/evg_prepare.sh`
(`evg-query get_my_hosts` and `evg spawn_host --name X`) with native
calls through the **stock `evergreen` CLI** plus ~150 LOC of orchestration
in `wt_ctl/domains/evg.py`.

After this change:

- `evg_prepare.sh` no longer searches `$CLAUDE_PLUGIN_ROOT`, the mck-dev
  marketplace path, or `~/.claude/plugins/cache/`. Single call:
  `wt-ctl evg spawn --name "${evg_host_name}"`.
- `wt_ctl/domains/evg.py` gains a `spawn()` method that's symmetric with
  the existing `terminate()` / `extend()` (all stock-CLI based).
- The only external dependencies are `evergreen` CLI + `~/.evergreen.yml`
  (token-based auth), both of which we already require.

Net cost: **+~150 LOC native Python, −~30 LOC bash, −1 plugin dependency.**

## Why this is feasible

`wt_ctl/domains/evg.py` already uses the stock `evergreen` CLI for
`host list --json`, `host terminate`, `host modify --extend-by`. Adding
`spawn` extends an existing pattern; no new auth surface, no new deps.

The only mck-dev value-add we replicate is the orchestration: resume
detection, poll-until-running, tag-after-AWS-id, display-name set, SSH
verify. All of that is shell-out to the stock CLI plus stdlib polling.

## Verify before coding (small unknowns)

1. **What does `evergreen host create` print on stdout?** Likely a line
   like `Host i-deadbeef spawned.` or similar. We need to either parse
   the host id from this, OR call `host list --mine --json` right after
   and pick the newest host without an existing display name.
   - Test:
     ```sh
     evergreen host create --distro ubuntu2204-latest-large \
         --key <your-key-name> --region eu-west-1
     ```
   - If parse is fragile, prefer the "list-after-create, pick newest by
     creationTime" path. The race window is tiny (we just spawned) so
     the heuristic is robust enough for a single-developer workflow.

2. **Default expiration of `evergreen host create`.** Stock CLI has no
   `--expiration` flag; the GraphQL mutation does. The CLI uses the
   server's default (probably 24h). We *could* clamp to 8h after create
   via... actually `host modify --extend` can only extend, not shrink.
   - Decision: accept the server default (24h-ish). Cost is irrelevant
     since `wt-ctl delete` terminates explicitly. If we ever care, we
     can add `evergreen host modify --no-expire` + scheduled-stop, or
     drop a follow-up ticket.

3. **Default key name.** mck-dev fetches `client.get_my_public_keys()[0]`
   from GraphQL. The stock CLI's `--key` takes either a literal public
   key body or a key name from `evergreen keys list`.
   - Decision: read `MCK_DEVC_EVG_KEY_NAME` env var; if unset, fall back
     to `$(evergreen keys list --json | jq -r '.[0].name')`. Document
     the env var in the plan / `evg_prepare.sh` help.

4. **Distro / region defaults.** mck-dev uses `ubuntu2204-latest-large`
   in `eu-west-1`. Match exactly.

## Code changes

### `scripts/dev/wt_ctl/domains/evg.py`

Add a `spawn()` method to `EvgDomain`. Signature:

```python
def spawn(
    self,
    *,
    name: str,
    distro: str = "ubuntu2204-latest-large",
    region: str = "eu-west-1",
    key_name: Optional[str] = None,
    poll_interval_s: float = 5.0,
    timeout_s: float = 600.0,
    emit: Optional[Callable[[str], None]] = None,
) -> str:
    """Spawn (or resume) an EVG host with the given display name.

    Idempotency contract: if a host with displayName==name already
    exists and isn't in {terminated, decommissioned, quarantined, failed},
    resume it (no new spawn). Otherwise spawn a fresh one, then poll
    until status==running, then tag and rename, then SSH-verify.

    Returns the host id (i-*). Raises ExternalCommandFailed / WtCtlError
    on terminal failure.
    """
```

Behavior, step-by-step (mirrors the working mck-dev flow but uses the
stock CLI):

1. **Resume detection.** Call `evergreen host list --mine --json`.
   Filter for `displayName == name AND status NOT IN
   {terminated, decommissioned, quarantined, failed}` (same predicate as
   `evg_prepare.sh` after commit `4fce8275a`). If found:
   - If status == "running", verify SSH and return the host id.
   - Else, skip the `host create` step and jump to the poll loop with
     the existing host id.

2. **Fresh spawn.** Resolve `key_name` (env var or `keys list --json
   | jq` fallback — but use stdlib, not jq, since we're in Python).
   Run:
   ```
   evergreen host create --distro <D> --key <K> --region <R>
   ```
   Parse the host id from stdout. If the parse fails, retry by calling
   `host list --mine --json` and taking the host with the most recent
   `creationTime` whose `id` starts with `evg-` or `i-` and whose
   `displayName` is empty.

3. **Poll.** Every `poll_interval_s`, call `host list --mine --json`,
   find the host by id. Break when `status == "running"`. Time out at
   `timeout_s`.

4. **Tag + display-name (once AWS gives us an `i-*` id).** When the
   polled host's `id` starts with `i-` and we haven't tagged it yet:
   ```
   evergreen host modify --host <id> --name <name> --tag name=<name>
   ```
   Best-effort; failure shouldn't abort the spawn (we'll retry next
   poll). Match mck-dev's tolerance here.

5. **SSH verify.** After status==running, run:
   ```
   ssh -o StrictHostKeyChecking=accept-new \
       -o ConnectTimeout=5 \
       ubuntu@<host.dnsName> true
   ```
   (Use the dnsName from the JSON, not the display name.) Retry up to
   ~30s; some hosts take a beat after status=running.
   - **Honor the existing SSH key policy** (memory:
     `feedback_evg_host_ssh_keys`): use the canonical evg-host key from
     `~/.ssh`, do NOT regenerate or auto-accept fresh fingerprints. If
     the SSH verify fails, the function returns the host id but logs a
     warning — terminate+re-spawn is the user's prerogative, not ours.

6. **Return** the final host id.

Helpers (private functions in `evg.py`):

```python
def _list_my_hosts_json(self) -> list[dict]: ...
def _resolve_key_name(self) -> str: ...
def _parse_host_id_from_create(self, stdout: str) -> Optional[str]: ...
def _find_host_by_id(self, hosts: list[dict], host_id: str) -> Optional[dict]: ...
def _ssh_verify(self, host: dict) -> bool: ...
```

All subprocess calls go through `self.runner` per the runner.py contract
(lint-enforced).

### `scripts/dev/wt_ctl/cli.py`

Add a `spawn` subcommand under `wt-ctl evg`:

```python
se_sp = se_sub.add_parser(
    "spawn",
    help="spawn (or resume) an EVG host with displayName=<name>.",
)
se_sp.add_argument("--name", required=True)
se_sp.add_argument("--distro", default="ubuntu2204-latest-large")
se_sp.add_argument("--region", default="eu-west-1")
se_sp.add_argument("--key", dest="key_name", help="Evergreen-managed key name (default: $MCK_DEVC_EVG_KEY_NAME or first key).")
```

Handler in `cmd_evg`:

```python
if sub == "spawn":
    host_id = ev.spawn(
        name=args.name,
        distro=args.distro,
        region=args.region,
        key_name=args.key_name,
        emit=lambda m: sys.stderr.write(m + "\n"),
    )
    print(host_id)
    return 0
```

### `scripts/dev/evg_prepare.sh`

Drop lines 65–106 (the mck-dev CLI locator block + the
`evg-query get_my_hosts | jq` resume check + the `evg spawn_host` call).
Replace with a single line:

```bash
echo "==> Spawning / resuming EVG host displayName='${evg_host_name}'"
"${worktree_root}/scripts/dev/wt-ctl" --quiet evg spawn --name "${evg_host_name}"
```

Keep the rest of `evg_prepare.sh` intact:
- Step 2: `.generated/.current-evg-host` pin (unchanged).
- Step 3: `make switch` regenerates context.env (unchanged).
- Step 4: `evg_host.sh ssh` smoke (unchanged — separate SSH verify
  against the worktree's full kubeconfig flow).
- Step 5: kind recreate (unchanged).

### Tests

`scripts/test/wt_ctl/test_evg_spawn.py` (new file):

- `test_resume_when_host_exists_running`: FakeRunner returns a
  `host list --mine --json` with a `displayName=="alpha"
  status="running"` row; assert no `host create` argv is emitted, return
  value matches existing host id.
- `test_resume_when_host_exists_starting`: same as above but
  status=="starting"; assert poll loop enters; runner returns
  status="running" on the second poll; assert no `host create`.
- `test_fresh_spawn_then_poll_until_running`: empty `host list`
  initially; FakeRunner records `host create` argv, simulates the host
  appearing in `host list` with successive statuses
  (`provisioning -> starting -> running`).
- `test_tag_and_rename_after_aws_id`: confirm
  `host modify --host i-X --name N --tag name=N` is emitted exactly
  once after the host's id transitions from `evg-` to `i-`.
- `test_terminal_failure_status_raises`: status=="failed" in poll loop
  -> raises `WtCtlError`.
- `test_timeout_raises`: poll never returns running; deadline hit ->
  raises `WtCtlError`.

Existing tests:
- `test_lint.py`: no change needed (we already gate on subprocess
  import; the new code uses `self.runner`).
- `test_orchestrator.py`: confirm the `evg_prepare` phase still
  invokes the (shorter) bash script.

### `docs/dev/wt-ctl-plan.md`

Add a one-line note at the bottom of "Phasing" that the mck-dev evg
dependency was removed; cross-reference this plan file. The plan file
itself can be deleted in the same commit (post-execution).

## Acceptance

- `python3 -m unittest discover -s scripts/test/wt_ctl -t scripts/test/wt_ctl`
  — all green (existing 71 + ~6 new).
- `python3 scripts/test/wt_ctl_lint.py` — clean.
- `bash scripts/test/wt_ctl_smoke.sh` — pass.
- `git grep -in "mck-dev\|CLAUDE_PLUGIN_ROOT\|evg-query\|evg_query_cli\|evg_cli" scripts/dev/`
  — empty (or only this plan file / commit-message historical references).
- **Wet test:** in a fresh worktree (e.g.
  `lsierant/wtctl-native-spawn-test`), run:
  ```
  wt-ctl create lsierant/wtctl-native-spawn-test
  ```
  Confirm the spawn phase completes, `.generated/.current-evg-host`
  is written, the host appears in `evergreen host list --mine`. Then
  `wt-ctl create lsierant/wtctl-native-spawn-test --resume` (idempotent
  re-run) — must NOT create a duplicate.
- **Wet test cleanup:**
  ```
  wt-ctl delete lsierant/wtctl-native-spawn-test
  ```
  Confirm the host is terminated.

## Commit policy

- Single commit titled `wt-ctl: native EVG spawn — drop mck-dev dep`.
- Body: describe the new `wt-ctl evg spawn` verb, list deleted symbols
  in `evg_prepare.sh`, and the env vars / defaults the user can tune.
- DO NOT push, DO NOT create JIRA tickets, DO NOT open PRs (per
  `feedback_devcontainer_fixes_no_tickets`).

## Out of scope

- Implementing the full `evergreen` GraphQL surface in-repo. We stick to
  shell-out to the stock CLI.
- Adding our own OIDC auth flow. The stock CLI handles auth via
  `~/.evergreen.yml`.
- Replacing `evg_host.sh ssh` (it's already stock-CLI / SSH-only;
  nothing mck-dev about it).
- Replacing the SSH host-key handling — must continue to use the
  canonical `~/.ssh/evg-host` key, per
  `feedback_evg_host_ssh_keys`. Do NOT ssh-keyscan.
- Replacing `kind` cluster recreation logic in `evg_prepare.sh`. That
  step is unrelated and stays bash.

## Risks / open questions

1. **`evergreen host create` stdout format.** Unknown until we run it
   live. Plan addresses this with a fallback (list-after-create heuristic).
2. **`--expiration` not supported in CLI.** Hosts will use the server
   default; if that's longer than mck-dev's 8h, cost is slightly higher
   but `wt-ctl delete` mitigates. Open follow-up: investigate if
   `host modify --extend -N` can shrink (unlikely — `--extend` is
   additive only).
3. **Key name resolution.** `evergreen keys list --json` exists; the
   fallback to "first key" matches mck-dev behavior. Make this
   deterministic across users by recommending the `MCK_DEVC_EVG_KEY_NAME`
   env var.
4. **Race window in fresh-spawn host-id parsing.** Mitigated by the
   list-after-create fallback + the fact that this is a single-developer
   workflow. Document the failure mode in the function docstring.

## Resume context for post-compaction execution

This file is self-contained — a future Claude session can pick this up
cold and execute it. Key paths:

- Domain to edit: `scripts/dev/wt_ctl/domains/evg.py` (134 LOC currently).
- Mck-dev spawn reference: `~/.claude/plugins/marketplaces/core-platforms-ai-tools/plugins/mck-dev/dev_tools/evergreen/high_level_api.py:870` (`spawn_host_cmd`).
- Bash to gut: `scripts/dev/evg_prepare.sh:65-106` (the mck-dev CLI
  locator + resume detection + spawn call). Lines 109+ (pin host name,
  `make switch`, kind recreate) stay.
- Existing tests to learn from: `scripts/test/wt_ctl/test_kfp.py` and
  `scripts/test/wt_ctl/_common.py` (FakePopenFactory pattern for
  subprocess mocking).
- Memory pointers:
  - `feedback_evg_host_ssh_keys.md` — SSH key policy
  - `feedback_evg_host_list.md` — use `evergreen host list`, not evg-query
  - `feedback_fail_fast_no_masking.md` — orchestrator must fail loud

After execution, this plan file can be deleted in the same commit (per
the precedent set by `wt-ctl-drop-legacy-plan.md`).
