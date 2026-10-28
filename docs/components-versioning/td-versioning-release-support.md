# Sub-TD 7: Versioning and release of mongodb-proxy and mongodb-reporter

Owner: Maciej Karaś

The new architecture adds two images. Both are built from the MCK repository and released with every MCK release at the MCK version. A customer's default install does not move to a new tag unless that component changed.

| Image | Used by | Operator env vars (names TBD) |
|---|---|---|
| mongodb-proxy | Every static database Pod, mounted into the mongodb container via OCI Images | MDB_PROXY_REPO_URL, MDB_PROXY_NAME, MDB_PROXY_VERSION, MDB_PROXY_LATEST_VERSION |
| mongodb-reporter | Headless only (AppDB and MongoDBCommunity), sidecar | MDB_REPORTER_REPO_URL, MDB_REPORTER_NAME, MDB_REPORTER_VERSION, MDB_REPORTER_LATEST_VERSION |

The env vars follow MDB_SEARCH_REPO_URL, MDB_SEARCH_NAME and MDB_SEARCH_VERSION. Both images get RELATED_IMAGE_* entries for OLM and image mirroring (see "How customers receive component versions").

## Terms

- **Component:** a supplementary image built from the MCK repository and released with MCK, such as mongodb-proxy and mongodb-reporter. Later additions follow the same rules. Init containers, the readiness probe and the upgrade hook are not migrated here (see Out of scope).
- **Latest:** the component version built by this MCK release. It always equals the MCK version.
- **Minimum:** the lowest component version this MCK release runs by default. It can be older than latest. It moves only when the component changed.
- **Component entry:** a changelog entry under `changelog/<component>/`.
- **Track latest:** one global switch that makes the operator use latest instead of the minimum.

## Goals

- A component fix is always built and published with the next MCK release, so every customer can get it.
- An MCK upgrade must not change a default (Helm or OLM) user's component tag unless the component changed: the proxy is mounted into every database Pod, so a new tag is a Pod template change and a rolling restart. The reporter does the same for AppDB and MongoDBCommunity Pods.
- A customer can opt in to latest, and back out, without extra mirroring work in disconnected clusters.
- Nobody has to remember to bump a version: a component code change without a component entry fails CI.
- Fixes must be backportable to older MCK lines without version clashes.

The goals apply to every component.

## Decision

- **Version scheme:** Component image tags are MCK versions (`mongodb-proxy:1.16.0`). No separate semver.
- **One MCK version:** The next MCK version is calculated once, by the existing `calculate_next_version`, from all changelog entries under `changelog/`, including `changelog/<component>/`. The highest kind decides the bump: breaking is a major, feature is a minor, anything else is a patch. A feature in a component therefore makes the MCK release a minor. Components never get their own version calculation.
- **Release:** Every MCK release builds, signs and publishes all components at the MCK version, like the init images today. The release pipeline does not change.
- **Versions in `release.json`:** Two fields per component. `proxyLatestVersion` always equals `mongodbOperator`. `proxyMinimumVersion` is the version used by default and can be older. Same for the reporter.
- **Minimum:** Derived from the changelog: any component entry added since the last MCK tag moves the minimum to the next MCK version. Every entry bumps, whatever its kind.
- **When it is computed:** In the PR, by the pre-commit hooks that already write `release.json`, the Helm values and the yaml. The release publishes what is already committed.
- **Defaults per channel:** Helm and OLM use the minimum. Static yaml uses latest, because yaml users cannot opt in easily. The cost is a new proxy tag and a rolling restart on every MCK upgrade for yaml users.
- **Opt-in:** One global track-latest switch, set by a Helm value, by the generated yaml, or by a `Subscription` env var in OLM. It resolves to this operator release's own component versions.
- **Manual version:** A version-based env var per component. Allowed, but unsupported.
- **OLM mirroring:** The bundle's `RELATED_IMAGE_*` entries list both the minimum and latest for each component, so opting in or out needs no manual `RELATED_IMAGE_*` work.
- **Enforcement:** A CI check fails a component code change without a component entry. depguard keeps the components isolated from the rest of the MCK code.
- **Delivery:** Only through MCK releases.
- **Backports:** Security and critical fixes go to `release-vN` as an ordinary MCK patch release (see Support).

Example for one component (illustrative: the proxy has not shipped):

| MCK release | Component entries since previous release | `proxyLatestVersion` | `proxyMinimumVersion` |
|---|---|---|---|
| 1.14.0 | first release | 1.14.0 | 1.14.0 |
| 1.14.1 | none | 1.14.1 | 1.14.0 |
| 1.15.0 | one fix | 1.15.0 | 1.15.0 |
| 1.15.1 | none | 1.15.1 | 1.15.0 |
| 1.16.0 | two entries | 1.16.0 | 1.16.0 |

Helm and OLM users get a new proxy tag in 1.14.0, 1.15.0 and 1.16.0 only. Yaml users and track-latest users get one in every release.

## How it works in a PR

The diagrams follow one PR from the previous release state to the committed diff: `docs/components-versioning/diagrams/pr-without-component-entry.html` and `docs/components-versioning/diagrams/pr-with-component-entry.html`. Version numbers in them are illustrative.

**PR without a component entry** (previous tag 1.14.0: latest 1.14.0, minimum 1.12.0):
- An operator-only fix. The component changelog check passes because no component code changed.
- The hook computes next MCK 1.14.1 and latest 1.14.1. No component entry exists since the tag, so the minimum stays 1.12.0.
- The diff: `release.json`, and the Helm and OLM values carry minimum 1.12.0 and latest 1.14.1. The static yaml carries latest 1.14.1.
- After an upgrade, Helm and OLM users on the default keep the same proxy tag, with no restart. Yaml users and track-latest users get 1.14.1 and a rolling restart.

**PR with a component entry:**
- The PR changes the proxy code and adds `changelog/proxy/...`. If the entry is missing, CI fails and the PR is blocked.
- The hook computes minimum 1.14.1 and latest 1.14.1.
- After an upgrade, every channel gets proxy 1.14.1 with a rolling restart. That restart is the cost of an entry.

## How the minimum is derived

Computed in `update_release.py`, next to the existing `mongodbOperator` propagation, so the same function runs on every commit and on every `release-vN` branch:

```
prev_minimum        = release.json[proxyMinimumVersion] at the last MCK tag on this branch
entries             = changelog/proxy/*.md added since the last MCK tag on this branch
proxyLatestVersion  = mongodbOperator
proxyMinimumVersion = proxyLatestVersion if len(entries) > 0 else prev_minimum
```

- Entries are counted from the last MCK tag, the same base MCK uses for its own version.
- Order: the hooks compute the next MCK version first, from all changelogs, and only then derive latest and minimum from it. Both are recomputed on every commit, so if a later PR raises the bump from patch to minor, a minimum equal to the next MCK version follows it.
- Component entries need no script change for the MCK version or the release notes, because `changelog/` is read recursively by `version.py` and `release_notes.py`. Tested in a temporary clone, starting from tag 1.14.0 with no entries since:

  | Entries since the tag | Next MCK version |
  |---|---|
  | none | 1.14.1 |
  | component `fix` or `other` | 1.14.1 |
  | component `feature` | 1.15.0 |
  | component `breaking` | 2.0.0 |
  | root `fix` and component `feature` | 1.15.0 |
  | component `fix` and another component's `feature` | 1.15.0 |

  Component entries also appear in the MCK release notes.
- Bootstrap: with no previous minimum, the first release that includes the component sets both fields to its own version.

## Changelog and enforcement

- **Layout:** `changelog/<component>/<date>_<kind>_<title>.md`, same format as today. `create_changelog.py` gets `--component`.
- **Validation (`check_changelog.py`):** entries are allowed only in a known component directory. An unknown subdirectory is an error, because a typo would mean the minimum never moves.
- **Component changelog check (CI):** a PR that changes component code, its Dockerfile, or an allowlisted shared package (see depguard) must add an entry under `changelog/<component>/`, or carry the `skip-changelog` label. It works like the existing MCK check. The path map per component lives in the repository (location TBD).
- **depguard:** a rule in `.golangci.yml` next to the two existing ones. The proxy imports no other MCK package. The reporter imports only a named allowlist, the readiness `health` types in `mongodb-community-operator/pkg/readiness/health`, so it does not duplicate the agent status format. Changes to an allowlisted package count as reporter changes.
- **Dependencies:** third-party dependency and Go toolchain changes are not path-bound. A reachable critical or high vulnerability in a component's dependencies needs a `fix` entry, so the minimum moves and default users get the fix in the next MCK release. Other dependency churn needs no entry and reaches customers through latest. This is a review rule that depends on author judgement and needs team agreement.

## Release pipeline

- Components behave like `initDatabaseVersion` today: `release.json` keeps the latest field equal to `mongodbOperator`, and staging, promotion, publishing, signing, SBOM and Trivy scans run for every release. No filter and no no-overwrite rule are needed.
- The SSDLC report keeps working unchanged. It keeps only image versions equal to `mongodbOperator`, which is what latest is.
- Registering a new image touches `build_info.json`, `build_info.py`, the `pipeline.py` builder map, `pipeline.sh`, the Evergreen build and `cve_scan_images` tasks, `evg-private-context`, the Makefile and `update_go_version.sh`.

## How customers receive component versions

All installation methods are generated from `release.json` at release time. Customers do not edit versions to upgrade: applying the newer chart, yaml or bundle moves the component tag only when the minimum moved, or always for yaml and track latest.

- **Helm:** the chart uses the minimum by default. A Helm value for the global track-latest switch sets the env var, and a Helm value per component sets a manual version. The minimum and latest are carried where `helm upgrade --reuse-values` cannot freeze them (mechanism TBD).
- **OLM:** the bundle is generated from the same chart with `values-openshift.yaml`, so it uses the same defaults as Helm. Opting in or out, and a manual version, are env vars in `Subscription.spec.config.env`. OLM overrides same-named env vars in the operator Pod.
- **Static yaml:** generated from the Helm template. It switches track latest on by default.
- **Resolution:** with track latest on, the operator uses `MDB_<COMPONENT>_LATEST_VERSION`. Otherwise it uses `MDB_<COMPONENT>_VERSION`, which defaults to the minimum and can be set manually.
- **Disconnected clusters:** the operator resolves images through `RELATED_IMAGE_<name>_<version>`, so both versions in the bundle work offline. A manual version outside the bundle needs the customer to mirror it and add the `RELATED_IMAGE_*` entry themselves.
- **Installation Unification:** these env vars are installation-stage settings. A future Operator Config (MCK 2.x) could host the switch, so the env vars must be added to its migration inventory.

## Support

- Supported: the minimum, and track latest. A manual component version is allowed at the customer's own risk and is not supported.
- Fixes ship only in a new MCK release. Backports follow the backporting policy (SPIKE: Support Backporting to Prior Major Release): security and critical fixes only, only to the latest minor of a previous major (`release-vN`), for one year after the next major. No backports to minor lines within a major.
- Backport flow: cherry-pick the fix and its component entry to `release-vN`. The hook sets the minimum to that branch's next MCK version (for example 1.20.1), and the release publishes all components at 1.20.1. There is no clash, because MCK versions are unique across branches. This first matters at MCK 2.0, if the proxy ships in 1.x.
- Compatibility: track latest resolves to this operator release's own component versions, which is the tested combination. The minimum runs a newer operator with an older component, so a smoke test gate runs the operator at HEAD against the minimum component images on every merge to master. A PR that changes the proxy API contract must carry a component entry.
- Release notes and docs state the minimum component versions of each MCK release.

## Single go.mod or one per component

Decision: keep the root `go.mod`. Revisit if the triggers below happen.

What a separate module would give us: knowing exactly when a component's dependencies changed, and control over them independent of the operator.

- The proxy prototype (branch `KUBE-530/mongodb-proxy`) uses the standard library plus `go.uber.org/zap`. With the single `go.mod`, `go list -deps ./cmd/mongodb-proxy` gives its exact module closure: `zap v1.28.0` and `multierr v1.11.0`. The image is `FROM scratch`, so there is no base image to track beyond the Go toolchain.
- The reporter needs a Kubernetes client (Pod get and patch), so its closure will overlap heavily with the operator's. A separate module would gain little there.
- The repository was deliberately consolidated into one module (CLOUDP-332943, #288) so that `go test ./...` covers all Go code.

| | Single `go.mod` | One `go.mod` per component |
|---|---|---|
| Knowing when dependencies changed | Compare the `go list -deps` closure (module and version) of the component | Diff of the component's `go.mod`; Dependabot PRs scoped to its directory |
| Dependency control | Versions shared with the operator; one Dependabot config. Weekly grouped PRs include modules the proxy does not use | Independent versions; duplicated Kubernetes dependencies for the reporter |
| Tooling coverage | `./...` covers everything: `govulncheck` workflow, `go vet ./...`, `golangci-lint-full`, Dependabot directory `/` | `./...` stops at the module boundary. Each module needs separate wiring in `govulncheck`, `go vet` and tests, `golangci-lint`, Dependabot and `update_go_version.sh`, or it is silently skipped |
| Maintenance | Lowest | Go version and toolchain bumps per module; cross-module imports need versions or `replace` |

Path-based detection of code changes already works inside one module: the component changelog check and depguard use the component's directories. What stays unsolved in either layout is a dependency or toolchain update: it moves the minimum only when someone writes an entry, as the vulnerability review rule above requires.

Revisit if: the proxy's dependency graph grows or diverges from the operator's, the reporter ends up with a small closure, or we want dependency updates to move the minimum automatically.

## Alternatives considered and discarded

| Solution | Biggest cons |
|---|---|
| Adoption only: build every commit, but promote and publish a component only in a release that changes it | Needs release pipeline changes (a publish filter and a never-overwrite rule). A fix without an entry is never published, so customers cannot get it even by opting in. |
| Lockstep: every component tag equals the MCK version and is always used | Every MCK upgrade changes the tag and restarts all database Pods, even with no component change. |
| A `defer` flag on changelog entries, to hold changes back and ship them in one batch | New changelog syntax, validation, version roll-up and release notes preview rules. Dropped as too complicated. The minimum already stays put without entries, so the only control is when an entry is added. |
| Independent semver, manual bump (the readiness probe pattern) | Bumps are manual, so dependencies go stale. A backport and master can both need `1.4.1` with different contents. |
| Independent semver, automated bump from changelog entries | Same backport clash; needs component tags and prefix-aware tag lookup. |
| Separate Go module per component (`proxy/vX.Y.Z` tags) | Gives dependency visibility, not a bump policy. Release tooling assumes one version tag, and the repository moved away from nested modules. See "Single go.mod or one per component". |
| Separate repository | The TD builds from the MCK repository; cross-repository e2e and release coordination. |
| Supported out-of-band version override | Needs a compatibility matrix between operator, component and agent versions. A manual version is allowed but unsupported instead. |

## Out of scope

- Blocking an MCK release on a reachable vulnerability in a component (fix or record a waiver).
- Scheduled scan of the minimum component images on every supported branch. Scans run today only when an image is built, so an older minimum is not rescanned after release.
- Automatic minimum bumps on dependency or toolchain updates: Dependabot-created entries and a dependency-closure check.
- Per-component track-latest switches.
- Holding component changes back to batch them: every component entry moves the minimum, so the only control is when the entry is added.
- Hosting the track-latest switch in the Operator Config (MCK 2.x).
- Supporting arbitrary manual component versions.
- Generating release notes for the released commit. `release.yml` generates them on the dispatch branch HEAD, not on `commit_sha`. This is an existing issue, to be investigated separately.
- Migrating init containers, `readiness-probe` and `upgrade-hook` (1.0.24 and 1.0.10) into this scheme.

## Work items

1. Changelog model: add component directories. Parse and validate (known component directory, unknown subdirectory is an error) in `scripts/release/changelog.py` and `check_changelog.py`; add `--component` to `create_changelog.py`.
2. Component changelog check: a CI check with a path map per component, failing a component code change without an entry (`skip-changelog` as the escape). Includes the reporter's allowlisted shared packages.
3. depguard rules for the proxy and the reporter in `.golangci.yml`.
4. `release.json` fields and minimum derivation: add `proxyLatestVersion`, `proxyMinimumVersion`, `reporterLatestVersion`, `reporterMinimumVersion`. Derive in `scripts/evergreen/release/update_release.py`; map them to Helm in `update_helm_values_files.py`; read them in `release_info.py`.
5. Register the component images in the build and release pipeline, like `init-database`: `build_info.json`, `build_info.py`, `pipeline.py`, `pipeline.sh`, Evergreen build and `cve_scan_images` tasks, `evg-private-context`, the Makefile and `update_go_version.sh`.
6. Operator: version-based env vars per component, the global track-latest switch, and image resolution through `ContainerImage()`. `main.go`, `pkg/images`.
7. Installation configs: env vars and values in `helm_chart/templates/operator.yaml`, `values.yaml` and `values-openshift.yaml`; `RELATED_IMAGE_*` entries for both minimum and latest per component; the yaml generated with track latest on; regenerate `public/*.yaml` and `config/manager/manager.yaml`; check the generated OLM bundle.
8. Tests: derivation scenarios (no entry keeps the minimum, an entry moves it, a backport on a release branch, a component `feature` bumps the MCK version by a minor and a component `breaking` by a major) in `scripts/release/tests/conftest.py` and `testdata/test_git_repo.mmd`; operator unit tests for version resolution.
9. Smoke test gate in Evergreen on merges to master: the operator at HEAD against the minimum component images.
10. Docs: state the minimum component versions in each MCK release's notes and public docs, and document track latest and manual versions per installation method.
11. Add the new env vars to the Installation Unification env-var migration inventory.

## Open questions

- Names: the `release.json` fields, the env vars and the Helm values.
- Helm `--reuse-values` keeps previous values for existing keys. How do the minimum and latest reach the template without being user-overridable chart values?
- Should the smoke test gate also run on `release-vN` branches?
- Does a `Subscription`'s config survive an OLM upgrade, and does the OpenShift console expose env config? Not verified.
- Who judges "reachable critical or high" for the vulnerability review rule, and does the team accept it as a review rule?
- Who owns the reporter's depguard allowlist?
- Mirror size: OLM and mirrored registries carry two tags per component per release.
