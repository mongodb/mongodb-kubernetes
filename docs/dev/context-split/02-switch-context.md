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
