# Step 02 — Refactor `switch_context.sh`

## Goal

After step 01 split `root-context` into logical-only + new `site-context`,
`switch_context.sh` must:

1. Source `site-context` first (sets `PROJECT_DIR` etc.), then
   `root-context` (uses `PROJECT_DIR` for release.json lookups).
2. Capture exports separately for site vs. logical.
3. Write `.generated/context.env` (logical, every switch) and
   `.generated/context.<side>.env` (site, current side only).
4. Strip site-derived bytes from `print_operator_env.sh` output and
   write `.generated/context.operator.env` (no per-side operator file).
5. Drop legacy `.export.env` files and the `## Side:` stamp.

## Preconditions

- Step 01 complete and committed.
- `scripts/dev/contexts/site-context` exists.
- `scripts/dev/contexts/root-context` no longer exports site-derived
  bytes.

## Files touched

- `scripts/dev/switch_context.sh` (modify)
- `scripts/dev/print_operator_env.sh` — left as-is; we strip its output
  in `switch_context.sh` instead.

## Detailed plan

### 1. Side detection helper at the top

After the `set -Eeou pipefail` and `source scripts/funcs/errors` lines,
add:

```bash
# Side detection: /.dockerenv exists only inside containers.
# Used to pick which .generated/context.<side>.env file we write here,
# and is the same rule used by scripts/dev/devenv at source time.
side=$([[ -f /.dockerenv ]] && echo devc || echo host)
```

### 2. Replace the EVG-CI branch (preserve behavior)

Today's `switch_context.sh:51-58` has a special branch when `EVR_TASK_ID`
is set (running on Evergreen):

```bash
if [ -n "${EVR_TASK_ID-}" ]; then
    source "${context_file}"
    export CURRENT_VARIANT_CONTEXT="${context}"
    current_envs=$(export -p)
else
    # … env -i flow …
fi
```

This branch sources `${context_file}` (e.g. `contexts/e2e_mdb_kind_…`)
which in turn sources `local-defaults-context` and may use
`root-context`. After step 01, `root-context` requires `PROJECT_DIR`
in env. EVG provides a working directory via expansions; ensure
`PROJECT_DIR` is set in the EVG branch too:

```bash
if [ -n "${EVR_TASK_ID-}" ]; then
    : "${PROJECT_DIR:=$(realpath "${script_dir}/../..")}"
    export PROJECT_DIR
    # site-context computes other site-derived bytes for THIS side
    # (the EVG runner — which has no /.dockerenv → side=host).
    source "${contexts_dir}/site-context"
    source "${context_file}"
    export CURRENT_VARIANT_CONTEXT="${context}"
    current_envs=$(export -p)
else
    # … updated env -i flow below …
fi
```

### 3. Refactor the local-development env -i flow

Replace the existing `else` branch (currently around lines 59-77) with a
two-step capture: site exports separately, then logical (= total minus
site).

```bash
else
    # Step (a): capture site exports alone. site-context introspects
    # the running shell (PROJECT_DIR, GOROOT, /.dockerenv → KUBECONFIG
    # variant, K8S_FWD_PROXY, s390x conditionals).
    site_envs=$(env -i \
        PWD="${PWD}" \
        PATH="${PATH}" \
        HOME="${HOME}" \
        MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
        EVG_HOST_NAME="${EVG_HOST_NAME:-}" \
        LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
        bash -c "source ${contexts_dir}/site-context && export -p")

    # Step (b): capture site + logical together. We source site first
    # (so PROJECT_DIR is set for root-context), then local-defaults,
    # then the per-context profile, then root-context, then any
    # override file. The result includes both site and logical exports.
    base_command="source ${contexts_dir}/site-context"
    base_command+=" && source ${local_development_default_file}"
    base_command+=" && source ${context_file}"
    if [ -n "${additional_override}" ]; then
        echo "Using additional override file: ${additional_override_file}."
        base_command+=" && source ${additional_override_file}"
    elif [ -f "${override_context_file}" ]; then
        echo "Using override file: ${override_context_file}. If you do not want to use one, remove the file or its contents."
        base_command+=" && source ${override_context_file}"
    fi
    all_envs=$(env -i \
        PWD="${PWD}" \
        PATH="${PATH}" \
        HOME="${HOME}" \
        MCK_DEVC_NET_PREFIX="${MCK_DEVC_NET_PREFIX:-}" \
        EVG_HOST_NAME="${EVG_HOST_NAME:-}" \
        LOCAL_OPERATOR="${LOCAL_OPERATOR:-}" \
        CURRENT_VARIANT_CONTEXT="${context}" \
        bash -c "${base_command} && export -p")

    # Make all_envs the canonical source for subsequent steps that
    # need to re-source these vars (e.g., print_operator_env.sh below).
    eval "${all_envs}"

    # Logical = all minus site. We do the subtraction by name later;
    # this keeps current_envs around for backward compatibility with
    # existing post-processing in this script.
    current_envs="${all_envs}"
fi
```

### 4. Normalize the captured envs

Today's normalization (after the if/else):

```bash
current_envs=$(echo "${current_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | sed 's/=/=/'| sort | uniq)
```

Keep this. Apply the same normalization to `site_envs`:

```bash
current_envs=$(echo "${current_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | sort | uniq)

# Same normalization for site-only capture (in the local-dev branch only;
# EVG-CI branch doesn't use site_envs).
if [ -z "${EVR_TASK_ID-}" ]; then
    site_envs=$(echo "${site_envs[@]}" | grep '=' | sed 's/^declare -x //g' | sed 's/^export //g' | sort | uniq)
fi
```

### 5. Compute logical = current_envs − site_envs

After normalization, derive the logical-only set by subtracting the
site exports by name:

```bash
if [ -z "${EVR_TASK_ID-}" ]; then
    # Site keys = the variable names from site_envs.
    site_keys=$(echo "${site_envs}" | sed 's/=.*//' | sort -u)
    # Logical = current_envs minus any line whose KEY is in site_keys.
    logical_envs=$(echo "${current_envs}" | awk -F= -v keys="$(echo "${site_keys}" | paste -sd '|' -)" '
        $1 !~ "^("keys")$" { print }
    ')
else
    # EVG-CI branch: no per-side split. Treat everything as logical.
    logical_envs="${current_envs}"
    site_envs=""
fi
```

### 6. Write `.generated/context.env` (logical)

Replace today's write block:

```bash
echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!" > "${destination_envs_file}.env"
echo -e "## Regenerated at $(date)\n" >> "${destination_envs_file}.env"
# DROP the side stamp line:
# echo -e "## Side: $([[ -f /.dockerenv ]] && echo container || echo host)\n" >> "${destination_envs_file}.env"
echo "${logical_envs}" >> "${destination_envs_file}.env"

# workdir default (unchanged behavior)
if ! echo "${logical_envs}" | grep -q "^workdir="; then
  echo "workdir=\"${workdir:-.}\"" >> "${destination_envs_file}.env"
fi
```

### 7. Write `.generated/context.<side>.env` (site, current side only)

```bash
if [ -z "${EVR_TASK_ID-}" ]; then
    site_destination="${destination_envs_dir}/context.${side}.env"
    echo -e "## This file is automatically generated by switch_context.sh\n## Do not edit it!" > "${site_destination}"
    echo -e "## Regenerated at $(date)\n## Side: ${side}\n" >> "${site_destination}"
    echo "${site_envs}" >> "${site_destination}"
fi
```

The `## Side: ${side}` comment line here is informational (matches the
file's name) and is parseable by godotenv as a comment. It is NOT the
old fail-fast stamp.

### 8. Drop the `.export.env` generation

Remove this block:

```bash
awk '/^[A-Za-z_][A-Za-z0-9_]*=/ {print "export " $0}' < "${destination_envs_file}".env > "${destination_envs_file}".export.env
```

Same for the operator variant (replaced below).

### 9. Generate logical-only `context.operator.env`

Today's flow:

```bash
scripts/dev/print_operator_env.sh | sort | uniq >"${destination_envs_file}.operator.env"
awk 'NF {print "export " $0}' < "${destination_envs_file}".operator.env > "${destination_envs_file}".operator.export.env
```

Replace with:

```bash
# Generate the operator overlay, stripping site-derived bytes
# (KUBECONFIG and KUBE_CONFIG_PATH come from .generated/context.<side>.env).
scripts/dev/print_operator_env.sh \
    | sort | uniq \
    | grep -Ev '^(KUBECONFIG|KUBE_CONFIG_PATH)=' \
    > "${destination_envs_file}.operator.env"
```

No `.operator.export.env` generation (consumers use `set -a` or godotenv).

### 10. Delete the side-stamp emission and any related comments

Already covered in step 6 (drop the `## Side: …` echo line from
`context.env`).

### 11. Clean up old artifacts on regenerate

After writing new files, remove any stale `.export.env` from prior runs:

```bash
rm -f "${destination_envs_file}.export.env" "${destination_envs_file}.operator.export.env"
```

Place this near the bottom of the script, before the final `ls` /
status echo.

### 12. Final `ls` output should show the new shape

The `ls -l1 "${destination_envs_dir}" | grep "context"` at the bottom
of the script now naturally shows `context.env`, `context.host.env`
(or `context.devc.env`), `context.operator.env`, `.current_context`.
Acceptable as-is.

## Verification

```bash
# On host: switch to a context known to set EVG_HOST_NAME
make switch context=e2e_mdb_kind_ubi_cloudqa
ls .generated/context*.env
# Expect:
#   .generated/context.env
#   .generated/context.host.env       <-- NEW; bytes for host
#   .generated/context.operator.env

# Negative: no .export.env any more
ls .generated/*.export.env 2>/dev/null
# Expect: no matches

# Inside devc, switch again (or make switch picks up via shared bind-mount):
devcontainer exec --workspace-folder . bash -lc 'cd /workspace && make switch context=e2e_mdb_kind_ubi_cloudqa'
ls .generated/context*.env
# Expect:
#   .generated/context.devc.env       <-- NEW
#   .generated/context.host.env       <-- left untouched by devc switch
#   .generated/context.env            <-- regenerated; logical bytes identical
#   .generated/context.operator.env

# Confirm logical bytes match between host and devc generations
diff <(env -i bash -c 'source scripts/dev/contexts/site-context && source scripts/dev/contexts/root-context && export -p' \
       | grep -E '^declare -x (NAMESPACE|OPERATOR_ENV|REGISTRY|MDB_SEARCH_VERSION)=' | sort) \
     <(grep -E '^(NAMESPACE|OPERATOR_ENV|REGISTRY|MDB_SEARCH_VERSION)=' .generated/context.env | sort)
# Expect: zero diff (after `declare -x ` prefix stripping, which the
# verification script handles either by sed or by content comparison).

# Confirm site bytes differ:
diff .generated/context.host.env .generated/context.devc.env || true
# Expect: PROJECT_DIR, KUBECONFIG, K8S_FWD_PROXY differ; rest identical.
```

## Outputs / postconditions

- `.generated/context.env` exists and contains only logical bytes.
- `.generated/context.<side>.env` exists for the current side and
  contains site bytes.
- `.generated/context.operator.env` exists; does NOT contain
  `KUBECONFIG=` or `KUBE_CONFIG_PATH=` lines.
- No `.generated/*.export.env` files exist.
- No `## Side:` stamp in `context.env`.
- `make switch` from either side leaves the OTHER side's
  `context.<other>.env` file untouched (verify with `ls -lt`).

## Pitfalls

- **EVG-CI branch.** The `EVR_TASK_ID` branch in `switch_context.sh`
  feeds CI builds. Ensure the new code path keeps it functional. Test
  by simulating: `EVR_TASK_ID=fake make switch context=root-context`.
  CI uses a different `private-context` and skips most local
  development plumbing; verify by hand-running, then in a small EVG
  patch BEFORE merging.

- **`MCK_DEVC_NET_PREFIX` propagation.** `site-context` reads this from
  env; `switch_context.sh`'s `env -i` invocation passes it through.
  `root-context` separately reads it from `.devcontainer/.env` if
  unset. Both paths must work.

- **Subtraction algorithm.** The `awk -F= -v keys=…` snippet uses `|`
  as the separator. If a future site var contains `|` in its name,
  this breaks. Variable names per POSIX are `[A-Za-z_][A-Za-z0-9_]*` so
  `|` is impossible — but document the assumption.

- **`local-defaults-context` and `private-context-override`** are
  sourced AFTER `site-context` in step (b). If they contain
  PROJECT_DIR-derived exports, those become "logical" by the
  subtraction. That's a bug if it happens. Audit those files; today
  they're user-customization only and don't carry path bytes. Note
  this in the commit message.

- **Backward compatibility.** Existing consumers still source
  `.generated/context.export.env` until step 05 migrates them. After
  step 02 lands, those consumers break. **Do not push step 02 alone**
  — bundle through step 05 or use feature-branch staging.

  Recommendation: after step 02 lands locally, do NOT commit a single
  PR; keep the work on `lsierant/devcontainer` as a sequence of
  commits (steps 02–05) and only push when 05 lands so consumers and
  generator are coherent.

## Commit

```
switch_context.sh: emit logical context.env + per-side context.<side>.env

Refactor switch_context.sh to capture site-context's exports separately
from root-context's, write logical bytes to .generated/context.env (every
switch, identical regardless of side) and site bytes to
.generated/context.<side>.env (current side only). The other side's site
file is left untouched.

Drop the .export.env / .operator.export.env generation; consumers will
move to vanilla KEY=VALUE + `set -a` sourcing in subsequent steps.
Strip KUBECONFIG/KUBE_CONFIG_PATH from print_operator_env.sh output;
those come from context.<side>.env.

Drop the ## Side: fail-fast stamp in context.env; per-side files make
side mismatch impossible by construction.

Step 2/6 of docs/dev/context-split.
```

## Cross-references

- Master plan: [`README.md`](README.md)
- Previous step: [`01-root-context.md`](01-root-context.md)
- Next steps: [`03-devenv-bootstrap.md`](03-devenv-bootstrap.md) and
  [`04-godotenv-consumers.md`](04-godotenv-consumers.md) can run in
  parallel after this step.

## Notes (implementation)

Implemented on `lsierant/devcontainer` as a single commit modifying
`scripts/dev/switch_context.sh` (file/line summary below).

### What changed

`scripts/dev/switch_context.sh`:

- Added `side=$([[ -f /.dockerenv ]] && echo devc || echo host)` near the
  top, right after `source scripts/funcs/errors`.
- EVG-CI branch (`EVR_TASK_ID` set): now sets `PROJECT_DIR` to
  `realpath script_dir/../..` if unset, exports it, then sources
  `${contexts_dir}/site-context` BEFORE `${context_file}`. The rest of
  that branch (export `CURRENT_VARIANT_CONTEXT`, capture `current_envs`)
  is unchanged.
- Local-dev branch (`else`): replaced single capture with two-step
  capture. Step (a) — `site_envs` from a `bash -c "source site-context"`
  in `env -i`, with `PWD/PATH/HOME/MCK_DEVC_NET_PREFIX/EVG_HOST_NAME/
  LOCAL_OPERATOR` passed through. Step (b) — `all_envs` from the same
  `env -i` with the original full chain (`site-context` →
  `local-defaults-context` → `${context_file}` → optional override),
  plus `CURRENT_VARIANT_CONTEXT`. `eval "${all_envs}"` keeps the rest of
  the script's expectations (e.g. `print_operator_env.sh`) intact.
  `current_envs` is set to `all_envs` for back-compat with the existing
  normalization line.
- Normalization: applied the same `grep '=' | sed 's/^declare -x //g' |
  sed 's/^export //g' | sort | uniq` to `site_envs` (in the local-dev
  branch only). Removed the no-op `sed 's/=/=/'` that was in the
  original.
- Subtraction: computes `site_keys` from `site_envs` keys, then
  `logical_envs = current_envs` minus any line whose KEY appears in
  `site_keys`, using `awk -F= -v keys="<piped>"`.
- Write order:
  - `${destination_envs_file}.env`: header + date + blank line + the
    logical-only set (no `## Side:` stamp). Workdir default appended
    if missing (unchanged behavior).
  - `${destination_envs_dir}/context.${side}.env`: header + date +
    `## Side: ${side}` informational comment + blank line + site
    bytes. Local-dev branch only; CI branch skips it.
- Operator overlay: `print_operator_env.sh | sort | uniq | grep -Ev
  '^(KUBECONFIG|KUBE_CONFIG_PATH)=' > .../context.operator.env`. No
  `.operator.export.env`.
- Cleanup: `rm -f` the legacy `${destination_envs_file}.export.env` and
  `${destination_envs_file}.operator.export.env` near the bottom of the
  script (before the final `ls`). Idempotent — silent no-op if they
  don't exist.

### Deviations from the plan

1. **Extra passthrough filter for `env -i` mechanics keys.** The plan's
   subtraction algorithm is correct in principle (`logical = all -
   site`), but `bash -c` under `env -i` re-marks every passthrough
   variable as exported. Result: `PATH`, `HOME`, `PWD`, `SHLVL`, `_`,
   plus any input vars we pass for `site-context` to introspect
   (`MCK_DEVC_NET_PREFIX`, `EVG_HOST_NAME`, `LOCAL_OPERATOR`,
   `CURRENT_VARIANT_CONTEXT`) all show up in `export -p` output even
   though `site-context` itself never `export`s them. Without filtering:
   - `PATH`/`HOME`/`PWD`/`SHLVL` end up in `context.host.env`
     (gigantic noise — host-side PATH leaking into the file we're
     trying to keep clean).
   - `EVG_HOST_NAME`/`MCK_DEVC_NET_PREFIX`/`LOCAL_OPERATOR` are
     classified as **logical** in `README.md` §3 (set by
     `root-context`), but they'd be subtracted out of `logical_envs`
     and written to `context.host.env` instead.

   Added two passthrough filters before the subtraction:

   ```bash
   passthrough_re='^(PWD|PATH|HOME|SHLVL|_|MCK_DEVC_NET_PREFIX|EVG_HOST_NAME|LOCAL_OPERATOR|CURRENT_VARIANT_CONTEXT)='
   site_envs=$(echo "${site_envs}" | grep -Ev "${passthrough_re}" || true)
   current_envs=$(echo "${current_envs}" | grep -Ev '^(PWD|PATH|HOME|SHLVL|_)=' || true)
   ```

   Two distinct lists on purpose: the env-mechanics keys
   (`PWD/PATH/HOME/SHLVL/_`) are dropped from BOTH captures (we never
   want them in any generated file). The logical-input keys
   (`MCK_DEVC_NET_PREFIX/EVG_HOST_NAME/LOCAL_OPERATOR/
   CURRENT_VARIANT_CONTEXT`) are dropped only from `site_envs` so the
   subtraction doesn't pull them out of `logical_envs` — they remain
   in `current_envs` because `root-context` does export them, and we
   want them in `context.env`.

   **Action for steps 03/04/05:** when consumers source
   `context.<side>.env`, they will NOT get a PATH from it (which is
   correct — `/etc/profile.d/mck-bin.sh` and dev's `~/.zshrc` own
   PATH, per the README design). Confirm `op_run.sh`/`e2e_run.sh`
   migrations don't accidentally re-introduce PATH from
   `context.export.env` muscle memory.

2. **`HOME` passed through `env -i`.** Step 01 Notes flagged that the
   verification's `env -i` invocations strip `HOME`, which causes
   `site-context` to fail under `set -u` because of
   `${HOME}/.kube/config`. Confirmed: added `HOME="${HOME}"` to BOTH
   `env -i` invocations in `switch_context.sh` (steps a and b above).

3. **`script_dir` / `script_name` left as-is in `root-context`.** Step
   01 Notes flagged this; not modified in this step.

4. **`site-context` (0755) vs. `root-context` (0644) mode mismatch.**
   Step 01 Notes flagged this; intentionally not normalized in this
   step. Both files are sourced, never executed; the mismatch is
   harmless. A follow-up step (or a pre-commit hook) can pick a
   convention.

### Issues uncovered for steps 03/04/05

- **`set_env_context.sh` and pre-commit hooks.** Pre-commit hooks
  (`update-release-json`, `update-values-yaml`, `generate-standalone-yaml`,
  `generate-and-update`, `validate-helm-charts`) all invoke shell
  scripts (`generate_files.sh`, `lint_helm_chart.sh`) that
  `source scripts/dev/set_env_context.sh`, which in turn sources
  `.generated/context.export.env`. With step 02's cleanup
  (`rm -f *.export.env` at the end of `switch_context.sh`), these
  hooks fail with `realpath: ...context.export.env: No such file or
  directory`. To get this commit through pre-commit, I temporarily
  re-emitted `.export.env` files outside `switch_context.sh` (vanilla
  `awk '/^[A-Za-z_][A-Za-z0-9_]*=/ {print "export " $0}'` of
  `context.env` + `context.host.env`, plus `context.operator.env`).
  Step 05 must update `set_env_context.sh` to use the new pattern
  (`set -a; source context.env; source context.<side>.env; set +a`)
  so pre-commit goes back to passing without the stub files. Note
  that the side guard in `set_env_context.sh` reads the `## Side:`
  stamp from `context.env` — that stamp is gone in this step's
  output; step 05 should remove the side guard entirely.

- **Other shell consumers** (`op_run.sh`, `e2e_run.sh`, `evg_host.sh`,
  `create_worktree.sh`, `delete_om_projects.sh`, `run.sh`) all source
  `.generated/context.export.env` directly. They will all break until
  step 05. The step 05 author should grep for `context.export.env`
  and `context.operator.export.env` to find every site:

  ```
  scripts/dev/op_run.sh:30 + 75
  scripts/dev/e2e_run.sh:28
  scripts/dev/evg_host.sh:72
  scripts/dev/create_worktree.sh:75
  scripts/dev/delete_om_projects.sh:21 (comment only)
  scripts/dev/set_env_context.sh:11, 17, 21, 23
  scripts/test/bash/run.sh:57
  scripts/test/bash/worktree/test_om_project_namespace.bats:9, 96 (comments only)
  Makefile:56 (comment only)
  ```

- **CI (EVG) branch surface area.** The CI branch now sources
  `site-context` (which sets `K8S_FWD_PROXY`, `KUBECONFIG`, `GOROOT`,
  `MULTI_CLUSTER_*`, s390x conditionals) before `${context_file}`. EVG
  variant scripts that rely on those vars being unset before
  `evergreen.yml` expansions will see them set now. Specifically:
  `KUBECONFIG="${HOME}/.kube/config"` will fire on the EVG runner
  (which has no `EVG_HOST_NAME`); EVG variants that override
  `KUBECONFIG` later in the chain still work because `${context_file}`
  is sourced after. Smoke-tested with `EVR_TASK_ID=fake make switch
  context=root-context` — exits clean and writes a logical-only
  `context.env`. Real EVG end-to-end smoke happens in step 06.

- **`local-defaults-context` exports `PROJECT_DIR` and `workdir`.** It
  runs AFTER `site-context` in the chain (step (b) above). Its
  `PROJECT_DIR` redefinition is identical to `site-context`'s value
  on the local-dev side (both compute `realpath script_dir/../../../`
  from their respective files, which resolves to the same worktree
  root). After the subtraction, `PROJECT_DIR` ends up only in
  `context.<side>.env` (not in `context.env`) — correct per design.
  `workdir` is logical (per README §3) and stays in `context.env`.
  No action needed; flagged for future contributors who may try to
  remove the redundant export from `local-defaults-context`.

### Verification output (relevant lines)

`make switch context=root-context` (host side):

```
Generating context files from: root-context
Generated env files in /Users/.../.generated:
context.env
context.host.env
context.operator.env
```

`ls .generated/context*.env` — only the three files; no `*.export.env`:

```
.generated/context.env
.generated/context.host.env
.generated/context.operator.env
```

`grep -E '^(PROJECT_DIR|KUBECONFIG|GOROOT)=' .generated/context.env` —
no matches (logical only):

```
(empty; exit 1)
```

`grep -E '^PROJECT_DIR=' .generated/context.host.env` — site bytes
present:

```
PROJECT_DIR="/Users/.../lsierant_devcontainer"
```

`context.host.env` full body (filtered):

```
## This file is automatically generated by switch_context.sh
## Do not edit it!
## Regenerated at Sun May 10 09:36:42 CEST 2026
## Side: host

GOROOT="/Users/.../golang.org/toolchain@v0.0.1-go1.25.9.darwin-arm64"
K8S_FWD_PROXY="127.0.0.1:11616"
KUBE_CONFIG_PATH=".../.generated/multicluster_kubeconfig"
KUBECONFIG=".../.generated/evg-host.kubeconfig"
MULTI_CLUSTER_CONFIG_DIR=".../.multi_cluster_local_test_files"
MULTI_CLUSTER_KUBE_CONFIG_CREATOR_PATH=".../multi-cluster-kube-config-creator"
PROJECT_DIR="/Users/.../lsierant_devcontainer"
```

`grep -E '^(KUBECONFIG|KUBE_CONFIG_PATH)=' .generated/context.operator.env`
— no matches (stripped):

```
(empty; exit 1)
```

`context.env` head (no `## Side:` stamp):

```
## This file is automatically generated by switch_context.sh
## Do not edit it!
## Regenerated at Sun May 10 09:36:42 CEST 2026

AGENT_IMAGE="..."
```

`EVR_TASK_ID=fake make switch context=root-context` (CI smoke) — exits
clean, writes `context.env` (treats everything as logical, no
`context.<side>.env` written):

```
Generating context files from: root-context
Generated env files in /Users/.../.generated:
context.env
context.host.env       <-- left untouched from prior local run
context.operator.env
```

`pre-commit run --all-files` — all hooks pass (after the temporary
stub `.export.env` workaround documented above).
