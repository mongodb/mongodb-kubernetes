# Evergreen CI/CD Configuration Guide

Each variant needs to be tagged with one or more tags referencing related build scenario:

- pr_patch: for patches created by GitHub PRs
- staging: for builds triggered when merging to master or release branch
- deprecated-release: legacy release process (rebuilds/retests everything), triggered by "old-X.Y.Z" git tags. To be
  removed soon — see .evergreen-release.yml.
- release-publish: new release process (publish-only, no rebuild/retest), triggered by "new-X.Y.Z" git tags. See
  .evergreen-release-publish.yml.

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

### Legacy release scenario (deprecated, to be removed soon):

Rebuilds and retests every image before publishing. Triggered when the tag annotation does NOT start with "[new]"; the
CLI patch below runs the same variants. See .evergreen-release.yml.

```shell
evergreen patch -p mongodb-kubernetes -a deprecated-release -d "Test release build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=release --param OPERATOR_VERSION=1.3.0-rc
```

### New release-publish scenario:

Publishes already-promoted images straight to production — no rebuild, no retest. Triggered when the tag annotation
starts with "[new]"; the CLI patch below runs the same variants. See .evergreen-release-publish.yml.

```shell
evergreen patch -p mongodb-kubernetes -a release-publish -d "Test release-publish build" -f -y -u --path .evergreen.yml --param BUILD_SCENARIO=release --param OPERATOR_VERSION=1.3.0-rc
```

### Release-style preflight only (no image push, no Pyxis submit)

Runs the same **`preflight_release_operator_images_task_group`** as a real tag release, but **does not** run *
*`release_images`** (nothing is built/pushed) and sets **`preflight_submit: false`** (check only).

You must pass versions for tags that **already exist on Quay** (typically match **`release.json`** on your branch: *
*`mongodbOperator`**, readiness hook versions, etc.).

```shell
evergreen patch -p mongodb-kubernetes -a preflight_release_test \
  -d "Test release preflight only" -f -y -u --path .evergreen.yml \
  --param BUILD_SCENARIO=release \
  --param OPERATOR_VERSION=1.8.0
```

Adjust probe/hook params to match **`release.json`** if your project does not set them by default on patches. *
*`mongodb-enterprise-server`** preflight still enumerates Quay tags (same as release).

### Custom agent URL

Pass `mdb_custom_agent_url` to override the agent version in all e2e variants (static and non-static). The URL must
point to a `.tar.gz` agent tarball; the version is extracted from the filename. No `release.json` changes are needed.

```shell
evergreen patch -p mongodb-kubernetes -a pr_patch -d "Test custom agent" -f -y -u --path .evergreen.yml \
  --param mdb_custom_agent_url=https://example.com/mongodb-mms-automation-agent-108.0.26.9047-1.rhel8_x86_64.tar.gz
```

## E2E Test Path Filtering on PRs

Evergreen uses two levels of file-based filtering on PRs:

- **Project-level `ignore:`** (top of `.evergreen.yml`): files that never trigger any build (e.g. `*.md`, `.github/**`,
  `changelog/**`). Add entries here for files that are purely non-functional.
- **Variant-level `paths:`** (`production_code_paths` anchor in `.evergreen.yml`): controls which variants run. E2e
  variants use a denylist (`**` minus `ci/**` and the release config) so they skip CI-only changes but run for
  everything else.

If your PR only touches ignored files, Evergreen sends a synthetic green status with no builds. If it touches
non-ignored files that don't match the e2e `paths:`, static/lint variants still run but e2e is skipped.

To add a new file type that should never trigger any build, add it to `ignore:` in `.evergreen.yml`. To make it skip e2e
but still run lint/static, add a negation to `production_code_paths` instead.

## PR Labels

Automatic PR checks are gated by GitHub PR labels via `required_labels` on the `github_pr_aliases` entries
in
`.evergreen.yml` ([Evergreen docs](https://docs.devprod.prod.corp.mongodb.com/evergreen/Project-Configuration/Project-and-Distro-Settings#label-based-pr-testing)).
Without any label only the always-on checks run: unit tests, pre-commit, and the OCP version drift check.

| Label                      | Variant tag(s)       | What runs                                                                                                      |
|----------------------------|----------------------|----------------------------------------------------------------------------------------------------------------|
| `evergreen:e2e-all`        | `e2e_test_suite`     | ~1000 tasks, complete e2e set (excludes multi-arch smoke tests)                                                |
| `evergreen:e2e-default`    | `e2e_test_suite`     | ~540 tasks, e2e-all minus static, OM7 and race variants; close to the former default PR set                    |
| `evergreen:e2e-medium`     | `e2e_test_suite`     | ~250 tasks tagged `e2e_medium`: subset of e2e-all across static/non-static, OM 7/8/9, multi-cluster         |
| `evergreen:e2e-light`      | `e2e_test_suite`     | ~125 tasks tagged `e2e_light`: smallest subset (strict subset of `e2e-medium`) for most PRs                    |
| `evergreen:e2e-search`     | `e2e_test_suite`     | All tasks named `e2e_search_*`: MongoDB Search suites across static, non-static, cloudqa and OM variants       |
| `evergreen:e2e-static`     | `static`             | Static-container e2e variants + shared init/build (`init_test_run`, `init_tests_with_olm`, `build_om*_images`) |
| `evergreen:e2e-non-static` | `non_static`         | Non-static e2e variants + shared init/build                                                                    |
| `evergreen:e2e-cloudqa`    | `cloudqa`            | Cloud QA variants (excluding OpenShift)                                                                        |
| `evergreen:e2e-community`  | `e2e_mco_test_suite` | Community suites (`e2e_mdb_community`, `e2e_mco_tests`)                                                        |
| `evergreen:om7`            | `om7`                | Ops Manager 7.0 variants                                                                                       |
| `evergreen:om9`            | `om9`                | Ops Manager 9.0 variants                                                                                       |

OpenShift variants (`e2e_openshift_test_suite`) are not selected by any PR label; they run on master commits and via
the `staging` alias.

Labels are evaluated when the PR patch is created; adding one afterwards injects the newly-satisfied tasks into the
existing patch in place. Removing a label is a no-op and never cancels running tasks. Variant `paths:` filtering still
applies on top — a labeled variant whose paths don't match the PR's changed files is skipped.

## Manual Variant Aliases

Some CI variants are excluded from the default PR run to save compute time. These variants run on master commits and
can be triggered manually via CLI aliases; most can also be pulled into a PR via the labels above.

### Test Suites

| Alias        | Description                                                   | Runs on PRs                           |
|--------------|---------------------------------------------------------------|---------------------------------------|
| `static`     | Static container variants (`e2e_static_*`, shared init/build) | With `evergreen:e2e-static` label     |
| `non_static` | Non-static container variants (shared init/build)             | With `evergreen:e2e-non-static` label |
| `om7`        | Ops Manager 7.0 variants (static and non-static)              | With `evergreen:om7` label            |
| `om9`        | Ops Manager 9.0 variants                                      | With `evergreen:om9` label            |
| `race`       | Race detector variant                                         | With `evergreen:e2e-all` label        |
| `smoke_test` | Smoke e2e suite (`patch-run` tasks)                           | No                                    |
| `cloudqa`    | Cloud QA non-static variants                                  | With `evergreen:e2e-cloudqa` label    |

### Release & Housekeeping

| Alias                             | Description                                                     |
|-----------------------------------|-----------------------------------------------------------------|
| `staging`                         | Full master-merge (staging) scenario                            |
| `preflight_release_test`          | Release-style preflight only (no image push, no Pyxis submit)   |
| `deprecated-release`              | Legacy release process (rebuilds and retests before publishing) |
| `release-publish`                 | New release process (publishes already-promoted images)         |
| `release_specific_agent_manually` | Release a specific agent version manually                       |
| `release_all_agents_manually`     | Release all agent versions manually                             |
| `release_current_agents_manually` | Release the current agent versions manually                     |
| `public_docs_snippets`            | Public docs snippets refresh (generation + PR creation)         |
| `smoke_test_release`              | Smoke suite against released images                             |
| `periodic_teardowns`              | Periodic teardown variants                                      |

### Usage

Run these aliases when your PR changes might affect the excluded variants:

```shell
# Run OM7 variants (static and non-static)
evergreen patch --alias om7

# Run race detector variant
evergreen patch --alias race

# Run all static container variants
evergreen patch --alias static
```

### When to Use

- **om7**: Changes affecting Ops Manager 7.0 compatibility
- **om9**: Changes affecting Ops Manager 9.0 compatibility
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

## CVE Image Scanning (trivy)

The build-and-publish flows run a `trivy_scan` step (function in `.evergreen-functions.yml`) that
invokes `scripts/release/trivy_scan.sh`. It resolves the image repositories/tags the same way the
build pipeline does (from `build_info.json` and the version env vars) and scans them with
[trivy](https://github.com/aquasecurity/trivy) (pinned in `scripts/evergreen/setup_trivy.sh`).
The step takes `image_names` (space-separated) so that **all the images of a flow are scanned in one
step and reported in a single Slack message**; `image_name` (a single image) is still supported.

Where scans run:

- **Staging builds**: the `cve_scan_images` task of the `init_test_run` variant runs after all the
  image build tasks and after the Ops Manager image builds (`build_om{70,80,90}_images`). It scans
  operator, init-ops-manager, init-database, database, readiness-probe, upgrade-hook, the Ops Manager
  images (7.0, 8.0 and 9.0, versions resolved from the `ops_manager_XX_latest` anchors by
  `scripts/release/trivy_scan.sh`) and the currently used agent versions. Only `release_om_and_agents`
  (released agents) still reports on its own.
- **New release process** (`release_publish` variant): after `mckci_release_publish_images`
  publishes the promoted image set to production, all the published images are scanned in a single
  step at their production refs with a fresh vulnerability DB. In dry-run releases the scan runs
  against the `REGISTRY` override instead.
- **Legacy release process** (`release_images` variant, deprecated): after each `release_*`
  rebuild task (one message per image).

PR/patch builds skip the scan by default (only `staging`/`release` build scenarios are
scanned). To force the staging scan on a patch (e.g. to test a trivy/pipeline change), run only the
build tasks and the scan with `TRIVY_SCAN_FORCE=true`:

```shell
evergreen patch -p mongodb-kubernetes -v init_test_run -t cve_scan_images -d "test trivy scan" -u -y \
  --param TRIVY_SCAN_FORCE=true
```

Slack notifications are only sent for mainline builds by default. To also exercise the Slack
send from a patch, add `--param TRIVY_SCAN_NOTIFY=true` (or `--param trivy_scan_flags="--dry-run"`
to print the message instead of sending it).

The Slack message lists the scanned images that have **fixable** vulnerabilities, one line per image,
e.g.:

```
- mongodb-kubernetes:abc1234: 50 vulnerabilities: 41 fixable (report)
- mongodb-agent:13.58.0.11326-1: 73 vulnerabilities: 57 fixable (report)
```

- The `image:tag` links to the entry of the image in its registry (the ECR console or quay.io, using the
  digest of the tag reported by trivy).
- `report` links to the raw trivy JSON of that image (the `linux/amd64` one when the image is scanned on
  several platforms). The reports are in a private bucket, so the link is presigned with the credentials of
  the `s3.put` step (`TRIVY_REPORTS_AWS_KEY`/`TRIVY_REPORTS_AWS_SECRET`) and **expires after 7 days**; the
  reports stay available from the Files tab of the task. Without credentials the link goes to the Files tab.

Images with no vulnerabilities, or with only unfixable ones, are not in the Slack message, and no
message is sent when no image has a fixable vulnerability. The task log still prints every image with
findings and its not fixable count. Findings are unioned across the image's platforms, so a vulnerability
is not counted once per platform. If a single image cannot be scanned the error is logged and the other
images are still reported. Long lists are split into several blocks of the same message to stay within
Slack's limits.

Raw trivy JSON reports (with the full per-CVE details) are written to `trivy-reports/` in the task
working directory and uploaded to S3 (bucket `operator-e2e-artifacts`, same bucket/pattern as
`upload_e2e_logs`) at `trivy-reports/${task_id}/${execution}/`, so findings remain inspectable even
for tasks that didn't trigger a Slack notification. They are available on the task page in the
Evergreen UI (Files tab). To run a scan locally:

```bash
scripts/evergreen/setup_trivy.sh
scripts/dev/run_python.sh scripts/release/trivy_scan.py operator agent --build-scenario release \
  --platform linux/amd64 --dry-run   # versions are taken from the environment (OPERATOR_VERSION, OM_VERSIONS, ...)
# or a single image with an explicit version:
scripts/dev/run_python.sh scripts/release/trivy_scan.py operator --build-scenario release \
  --version 1.5.0 --platform linux/amd64 --dry-run
```
