# Evergreen CI/CD Configuration Guide

Each variant needs to be tagged with one or more tags referencing related build scenario:
 - pr_patch: for patches created by GitHub PRs
 - staging: for builds triggered when merging to master or release branch
 - release: for builds triggered on git tags

For variants that are **only** triggered manually (patch) or by PCT we should use "manual_patch" tag.
Examples: `migrate_all_agents`, `e2e_operator_perf` or `publish_om80_images`.

This configuration allows us to run all the associated tasks for each scenario from evergreen command line.
This is especially helpful when making changes to `staging` or `release` variants and testing them using Evergreen
command line patches. For example there is no other way to trigger tasks that are run on merges to master other than
combining them together using aliases. The same applies for tasks being run on git tags.

See https://docs.devprod.prod.corp.mongodb.com/evergreen/Project-Configuration/Project-and-Distro-Settings#project-aliases

# Running Evergreen build scenarios

Use `BUILD_SCENARIO` parameter to select the build scenario you want to run. Based on the scenario selected the
appropriate `REGISTRY` and `OPERATOR_VERSION` values will be selected. `REGISTRY` and `OPERATOR_VERSION` can be
individually overridden as well. Overriding `OPERATOR_VERSION` is mostly applicable for `release` scenario, where
there is no git tag to pick up.

## Example commands

### Full patch scenario:

```shell
evergreen patch -p mongodb-kubernetes -a pr_patch -d "Test PR patch build" -f -y -u --path .evergreen.yml
```

### Staging scenario:

```shell
evergreen patch -p mongodb-kubernetes -a staging -d "Test staging build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=staging
```

### Release scenario:

```shell
evergreen patch -p mongodb-kubernetes -a release -d "Test release build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=release --param OPERATOR_VERSION=1.3.0-rc
```

### Release-style preflight only (no image push, no Pyxis submit)

Runs the same **`preflight_release_operator_images_task_group`** as a real tag release, but **does not** run **`release_images`** (nothing is built/pushed) and sets **`preflight_submit: false`** (check only).

You must pass versions for tags that **already exist on Quay** (typically match **`release.json`** on your branch: **`mongodbOperator`**, readiness hook versions, etc.).

```shell
evergreen patch -p mongodb-kubernetes -a preflight_release_test \
  -d "Test release preflight only" -f -y -u --path .evergreen.yml \
  --param BUILD_SCENARIO=release \
  --param OPERATOR_VERSION=1.8.0
```

Adjust probe/hook params to match **`release.json`** if your project does not set them by default on patches. **`mongodb-enterprise-server`** preflight still enumerates Quay tags (same as release).

## E2E Test Path Filtering on PRs

E2e build variants are automatically skipped on PRs that only touch non-production files (CI tooling, docs, YAML config, shell scripts, etc.). The filter triggers e2e runs when any changed file matches `**/*.go` or `**/*.py` and is not under `ci/`. The patterns are defined in the `production_code_paths` anchor in `.evergreen.yml`.

If your PR touches files outside those patterns but still needs e2e coverage, you can trigger e2e manually like this:

```shell
evergreen patch -p mongodb-kubernetes -a pr_patch_e2e -d "Manual e2e run" -f -y -u
```

To permanently add a new file type, extend the `paths:` list in the `production_code_paths` anchor in `.evergreen.yml`.

## Manual Variant Aliases

Some CI variants are excluded from automatic PR runs to save compute time. These variants only run automatically on master commits but can be triggered manually when needed.

### Available Aliases

| Alias | Description | Runs on PRs | Variants Included |
|-------|-------------|-------------|-------------------|
| `static` | Static container variants | Yes | `e2e_static_mdb_kind_ubi_cloudqa`, `e2e_static_multi_cluster_*`, etc. |
| `om7` | Ops Manager 7.0 variants | **No** | `e2e_om70_kind_ubi`, `e2e_static_om70_kind_ubi`, `build_om70_images` |
| `race` | Race detector variant | **No** | `e2e_operator_race_ubi_with_telemetry` |

### Usage

Run these aliases when your PR changes might affect the excluded variants:

```shell
# Run OM7 variants (static and non-static)
evergreen patch --alias om7

# Run race detector variant
evergreen patch --alias race

# Run all static container variants (convenience alias, already runs on PRs)
evergreen patch --alias static
```

### When to Use

- **om7**: Changes affecting Ops Manager 7.0 compatibility
- **race**: Changes to concurrent code, goroutines, or shared state

## Quarantined Tests

E2e tests blocked on a known issue outside our control (e.g. an upstream regression) are
tagged `quarantined` and set `patchable: false` in `.evergreen-tasks.yml`. This means they
never run in PR or manual patches, but **do** run automatically on every master merge commit,
so a known failure still correctly marks that commit as not releasable — quarantining never
silently hides a failure from the release gate.

List quarantined tests: `grep -B1 'tags:.*"quarantined"' .evergreen-tasks.yml` (or filter by
tag `quarantined` in the Evergreen UI). Each test file has a comment linking the tracking
ticket.

Because of `patchable: false`, these tasks cannot be force-run in a patch — the only way to
check whether the upstream issue is fixed is to look at the task's result on master, or
temporarily flip `patchable: true` in a throwaway branch.
