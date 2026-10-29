# Engineering Proposal: Versioning and release of MCK components

Owner: Maciej Karaś

This proposal covers how MCK versions and releases the supplementary images (components) that it builds from its own repository. Today the readiness probe and the version upgrade hook are bumped by hand, and the init images change with every MCK release. The proposal makes all of them follow MCK releases, so a default install changes a component tag only when that component changed.

It is independent of the Cloud Native Agent project. `mongodb-proxy` and `mongodb-reporter`, introduced there, are referenced as the first new components and as examples, but they are not part of this proposal.

| Component | Status | Used by |
|---|---|---|
| readiness probe | Existing, in scope. Versioned by hand today (1.0.24) | MongoDBCommunity Pods |
| version upgrade hook | Existing, in scope. Versioned by hand today (1.0.10) | MongoDBCommunity Pods |
| init-database | Existing, in scope. Changes with every MCK release today | Init container of non-static database Pods and of AppDB Pods. Carries the readiness probe, the version upgrade hook and the agent launcher scripts |
| init-ops-manager | Existing, in scope. Changes with every MCK release today | Init container of Ops Manager Pods. Carries the Ops Manager configuration tool and the backup daemon probes |
| mongodb-proxy | New, from the Cloud Native Agent project. Referenced here, not part of this proposal | Every static database Pod, mounted into the mongodb container via OCI Images |
| mongodb-reporter | New, from the Cloud Native Agent project. Referenced here, not part of this proposal | Headless only (AppDB and MongoDBCommunity), sidecar |

Subtabs: [Single go.mod or one per component](td-versioning-release-support-go-modules.md) and [Work items](td-versioning-release-support-work-items.md).

## Terms

- **Component:** a supplementary image built from the MCK repository and released with MCK: the readiness probe, the version upgrade hook, the init images (`init-database` and `init-ops-manager`), and later additions such as the proxy and the reporter.
- **Latest:** the component version built by this MCK release. It always equals the MCK version.
- **Minimum:** the lowest component version this MCK release runs by default. It can be older than latest. It moves only when the component changed.
- **Component entry:** a changelog entry under `changelog/<component>/`.
- **Track latest:** one global switch that makes the operator use latest instead of the minimum.

## Goals

- A component fix is always built and published with the next MCK release, so every customer can get it.
- An MCK upgrade must not change a default (Helm or OLM) user's component tag unless the component changed: a new component tag changes the Pod template and causes a rolling restart. For example, the proxy is mounted into every database Pod, and today every MCK upgrade changes the init image tags of all non-static databases, AppDBs and Ops Managers.
- A customer can opt in to latest, and back out, without extra mirroring work in disconnected clusters.
- Engineers must not need to remember to bump a version. The tooling spots a component change that lacks the required changelog entry and fails CI.
- The existing components follow the same scheme: the readiness probe and the version upgrade hook are no longer bumped by hand, and the init images no longer change tag on every MCK upgrade.
- Fixes must be backportable to older MCK lines without version clashes.

The goals apply to every component.

## Decision

- **Version scheme:** Component image tags are MCK versions (for example `mongodb-proxy:1.16.0`). No separate semver.
- **Source:** Same repository, same root `go.mod` (see the go.mod subtab).
- **One MCK version:** The next MCK version is calculated once, by the existing `calculate_next_version`, from all changelog entries under `changelog/`, including `changelog/<component>/`. The highest kind decides the bump: breaking is a major, feature is a minor, anything else is a patch. A feature in a component therefore makes the MCK release a minor. Components never get their own version calculation.
- **Release:** Every MCK release builds, signs and publishes all components at the MCK version, as the init images already do. The release pipeline does not change.
- **Versions in `release.json`:** Two fields per component, for example `proxyLatestVersion` and `proxyMinimumVersion`. The latest field always equals `mongodbOperator`. The minimum field is the version used by default and can be older. They replace the hand-edited `readinessProbeVersion` and `versionUpgradeHookVersion`. For the init images, the existing lockstep keys `initDatabaseVersion` and `initOpsManagerVersion` become the latest fields.
- **Minimum:** Derived from the changelog: any component entry added since the last MCK tag moves the minimum to the next MCK version. Every entry bumps, whatever its kind.
- **Manual bump:** The minimum can also be bumped on purpose, for example to ship a dependency fix. Add a component entry with `create_changelog.py --component <name>`. Every entry bumps, so no separate mechanism is needed.
- **When it is computed:** In the PR, by the pre-commit hooks that already write `release.json`, the Helm values and the yaml. The release publishes what is already committed.
- **Defaults per channel:** Helm and OLM use the minimum. Static yaml uses latest, because yaml users cannot opt in easily. The cost is a new component tag and a rolling restart on every MCK upgrade for yaml users.
- **Opt-in:** One global track-latest switch, set by a Helm value, by the generated yaml, or by a `Subscription` env var in OLM. It resolves to this operator release's own component versions.
- **Manual version:** A version-based env var per component. Allowed, but unsupported.
- **OLM mirroring:** The bundle's `RELATED_IMAGE_*` entries list both the minimum and latest for each component, so opting in or out needs no manual `RELATED_IMAGE_*` work.
- **Enforcement:** A CI check fails a component code change without a component entry. depguard keeps the components isolated from the rest of the MCK code.
- **Delivery:** Only through MCK releases.
- **Backports:** Security and critical fixes go to `release-vN` as an ordinary MCK patch release (see Support).

Example for one component, here the proxy (illustrative: it has not shipped):

| MCK release | Component entries since previous release | `proxyLatestVersion` | `proxyMinimumVersion` |
|---|---|---|---|
| 1.14.0 | first release | 1.14.0 | 1.14.0 |
| 1.14.1 | none | 1.14.1 | 1.14.0 |
| 1.15.0 | one fix | 1.15.0 | 1.15.0 |
| 1.15.1 | none | 1.15.1 | 1.15.0 |
| 1.16.0 | two entries | 1.16.0 | 1.16.0 |

Helm and OLM users get a new proxy tag in 1.14.0, 1.15.0 and 1.16.0 only. Yaml users and track-latest users get one in every release.

## How it works in a PR

The diagrams follow one PR from the previous release state to the committed diff. Version numbers in them are illustrative.

- Interactive versions: hosted at https://monglify.aix.prod.corp.mongodb.com/ (links TBD after upload).
- Static images: PNG exports of the same diagrams (TBD).
- Sources: `docs/components-versioning/diagrams/`.

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
- Component entries need no script change for the MCK version or the release notes, because `changelog/` is read recursively by `version.py` and `release_notes.py`. Tested in a temporary clone, starting from tag 1.14.0 with no entries since (table below).
- Component entries appear in the MCK release notes when they exist. A component without entries adds nothing.
- Bootstrap: with no previous minimum, the first release that includes the component sets both fields to its own version. For the existing components the minimum is seeded from the version of the last MCK release (readiness probe 1.0.24, upgrade hook 1.0.10, and the last released version for the init images), so migrating changes no tag for default users.

| Entries since the tag | Next MCK version |
|---|---|
| none | 1.14.1 |
| component `fix` or `other` | 1.14.1 |
| component `feature` | 1.15.0 |
| component `breaking` | 2.0.0 |
| root `fix` and component `feature` | 1.15.0 |
| component `fix` and another component's `feature` | 1.15.0 |

## Changelog and enforcement

- **Layout:** `changelog/<component>/<date>_<kind>_<title>.md`, same format as today.
- **Tooling:** the changelog creation tool, `create_changelog.py`, gets a `--component <name>` flag and creates the entry in `changelog/<component>/`.
- **Validation (`check_changelog.py`):** entries are allowed only in a known component directory. An unknown subdirectory is an error, because a typo would mean the minimum never moves.
- **Component changelog check (CI):** a PR that changes component code, its Dockerfile, or an allowlisted shared package (see depguard) must add an entry under `changelog/<component>/`, or carry the `skip-changelog` label. It works like the existing MCK check. The path map per component lives in the repository (location TBD). A component's paths include the code it embeds: `init-database` builds the readiness probe and the version upgrade hook from `mongodb-community-operator/cmd/readiness`, `cmd/versionhook` and their packages, so a change there needs an entry for the probe or the hook and for `init-database`. Otherwise a probe fix would never reach default users of the init image.
- **depguard:** a rule per component in `.golangci.yml`, next to the two existing ones. A component imports only its own packages and a named allowlist of other MCK packages. New components start isolated: the proxy has no allowlist, and the reporter allows the readiness `health` types in `mongodb-community-operator/pkg/readiness/health`, so it does not duplicate the agent status format. The existing components already share packages with the community operator: the readiness probe imports `mongodb-community-operator/pkg/readiness/{config,headless,health}` and the version upgrade hook imports `mongodb-community-operator/pkg/agent`, so those go on their allowlists. `init-database` builds from the same community operator code, and `init-ops-manager` imports no MCK package. A change in an allowlisted package counts as a change of the component.
- **Dependencies:** third-party dependency and Go toolchain changes are not path-bound. A reachable critical or high vulnerability in a component's dependencies needs a `fix` entry, so the minimum moves and default users get the fix in the next MCK release. Other dependency churn needs no entry and reaches customers through latest. This is a review rule that depends on author judgement and needs team agreement.

## Release pipeline

- Components behave like the init images today: `release.json` keeps the latest field equal to `mongodbOperator`, and staging, promotion, publishing, signing, SBOM and Trivy scans run for every release. No filter and no no-overwrite rule are needed.
- The existing components are already registered in `build_info.json`. For the readiness probe and the version upgrade hook, `version-ref` moves from the hand-edited field to the latest field. The init images already point at a lockstep field, which becomes their latest field.
- Registering a new component touches `build_info.json`, `build_info.py`, the `pipeline.py` builder map, `pipeline.sh`, the Evergreen build and `cve_scan_images` tasks, `evg-private-context`, the Makefile and `update_go_version.sh`.

## How customers receive component versions

All installation methods are generated from `release.json` at release time. Customers do not edit versions to upgrade: applying the newer chart, yaml or bundle moves the component tag only when the minimum moved, or always for yaml and track latest.

- **Helm:** the chart uses the minimum by default. A Helm value for the global track-latest switch sets the env var, and a Helm value per component sets a manual version. The minimum and latest are carried where `helm upgrade --reuse-values` cannot freeze them (mechanism TBD).
- **OLM:** the bundle is generated from the same chart with `values-openshift.yaml`, so it uses the same defaults as Helm. Opting in or out, and a manual version, are env vars in `Subscription.spec.config.env`. OLM overrides same-named env vars in the operator Pod.
- **Static yaml:** generated from the Helm template. It switches track latest on by default.
- **Resolution:** with track latest on, the operator uses `MDB_<COMPONENT>_LATEST_VERSION`. Otherwise it uses `MDB_<COMPONENT>_VERSION`, which defaults to the minimum and can be set manually. The env vars follow `MDB_SEARCH_REPO_URL`, `MDB_SEARCH_NAME` and `MDB_SEARCH_VERSION`.
- **Disconnected clusters:** the operator resolves images through `RELATED_IMAGE_<name>_<version>`, so both versions in the bundle work offline. A manual version outside the bundle needs the customer to mirror it and add the `RELATED_IMAGE_*` entry themselves.
- **Installation Unification:** these env vars are installation-stage settings. A future Operator Config (MCK 2.x) could host the switch, so the env vars must be added to its migration inventory.

## Support

- Supported: the minimum, and track latest. A manual component version is allowed at the customer's own risk and is not supported.
- Fixes ship only in a new MCK release. Backports follow the backporting policy (SPIKE: Support Backporting to Prior Major Release): security and critical fixes only, only to the latest minor of a previous major (`release-vN`), for one year after the next major. No backports to minor lines within a major.
- Backport flow: cherry-pick the fix and its component entry to `release-vN`. The hook sets the minimum to that branch's next MCK version (for example 1.20.1), and the release publishes all components at 1.20.1. There is no clash, because MCK versions are unique across branches.
- Compatibility: track latest resolves to this operator release's own component versions, which is the tested combination. The minimum runs a newer operator with an older component, so a smoke test gate runs the operator at HEAD against the minimum component images on every merge to master. A PR that changes a component's API contract must carry a component entry. For the init images that includes the scripts and arguments the operator relies on, and the operator constants that `init-ops-manager` duplicates (marked "keep in sync" in `mmsconfiguration`).
- Release notes and docs state the minimum component versions of each MCK release.

## Alternatives considered and discarded

| Solution | Biggest cons |
|---|---|
| Adoption only: build every commit, but promote and publish a component only in a release that changes it | Needs release pipeline changes (a publish filter and a never-overwrite rule). A fix without an entry is never published, so customers cannot get it even by opting in. |
| Lockstep: every component tag equals the MCK version and is always used | Every MCK upgrade changes the tag and restarts all database Pods, even with no component change. |
| A `defer` flag on changelog entries, to hold changes back and ship them in one batch | New changelog syntax, validation, version roll-up and release notes preview rules. Dropped as too complicated. The minimum already stays put without entries, so the only control is when an entry is added. |
| Independent semver, manual bump (the readiness probe pattern today) | Bumps are manual, so dependencies go stale. A backport and master can both need `1.4.1` with different contents. |
| Independent semver, automated bump from changelog entries | Same backport clash; needs component tags and prefix-aware tag lookup. |
| Separate Go module per component (`proxy/vX.Y.Z` tags) | Gives dependency visibility, not a bump policy. Release tooling assumes one version tag, and the repository moved away from nested modules. See the go.mod subtab. |
| Separate repository | Builds from the MCK repository; cross-repository e2e and release coordination. |
| Supported out-of-band version override | Needs a compatibility matrix between operator, component and agent versions. A manual version is allowed but unsupported instead. |

## Out of scope

- The proxy and the reporter themselves. They belong to the Cloud Native Agent project and only adopt this scheme.
- Blocking an MCK release on a reachable vulnerability in a component (fix or record a waiver).
- Scheduled scan of the minimum component images on every supported branch. Scans run today only when an image is built, so an older minimum is not rescanned after release.
- Automatic minimum bumps on dependency or toolchain updates: Dependabot-created entries and a dependency-closure check.
- Per-component track-latest switches.
- Holding component changes back to batch them: every component entry moves the minimum, so the only control is when the entry is added.
- Hosting the track-latest switch in the Operator Config (MCK 2.x).
- Supporting arbitrary manual component versions.
- Generating release notes for the released commit. `release.yml` generates them on the dispatch branch HEAD, not on `commit_sha`. This is an existing issue, to be investigated separately.

## Open questions

- Helm `--reuse-values` keeps previous values for existing keys. How do the minimum and latest reach the template without being user-overridable chart values?
- Who judges "reachable critical or high" for the vulnerability review rule, and does the team accept it as a review rule?
- The readiness probe and the version upgrade hook are configured with full-image env vars today (`READINESS_PROBE_IMAGE`, `VERSION_UPGRADE_HOOK_IMAGE`). Do they move to the version-based env vars, or keep the full-image form?
- Mirror size: OLM and mirrored registries carry two tags per component per release.
