# Sub-TD 7: Versioning and release of mongodb-proxy and mongodb-reporter

The new architecture adds two images. Both are built from the MCK repository and have their own versions, pinned in release.json.

| Image | Used by | Helm values | Operator env var |
|---|---|---|---|
| mongodb-proxy | Every static database Pod, mounted into the mongodb container via OCI Images | proxy.name, proxy.version | MDB_PROXY_IMAGE |
| mongodb-reporter | Headless only (AppDB and MongoDBCommunity), sidecar | reporter.name, reporter.version | MDB_REPORTER_IMAGE |

The names follow the existing agent.name and agent.version values. The env vars follow MDB_SEARCH_IMAGE. Both images get RELATED_IMAGE_* entries for OLM and image mirroring.

## Goals

- Vulnerability and dependency fixes must reach customers without relying on someone remembering to bump a version by hand (today `readinessProbeVersion` and `versionUpgradeHookVersion` are bumped manually).
- An MCK upgrade must not change the proxy version tag unless the proxy changed: the proxy is mounted into every database Pod, so a new tag is a Pod template change and a rolling restart. The reporter does the same for AppDB and MongoDBCommunity Pods.
- Fixes must be backportable to older MCK lines without version clashes.

The problems listed apply to every component image built from the MCK repository that is not the operator: `mongodb-proxy`, `mongodb-reporter`, and any later addition.

## Decision

- **Version scheme:** Component image tags are MCK versions (`mongodb-proxy:1.16.0`). No separate semver.
- **Source:** Same repository, same root `go.mod` (see "Single go.mod or one per component").
- **Versions in `release.json`:** Two fields per component. `proxyVersion` always equals `mongodbOperator`: the version this release would publish. `proxyPinnedVersion` is the version this MCK release actually uses; it can be older. Same for the reporter. Helm, OLM, standalone yaml and `release_info.py` read the pinned field.
- **Publishing:** A component image is built per commit in staging as today, but promoted and published only in a release that adopts it (`proxyPinnedVersion` equals `proxyVersion`). Tags have gaps by design.
- **Adoption:** Derived from the changelog, not set by hand: an entry in `changelog/<component>/` adopts the component in the next MCK release.
- **Delivery:** Only through MCK releases. No supported way to change a component version without upgrading the operator.
- **Backports:** Security and critical fixes go to `release-vN` as an ordinary MCK patch release (see Support).

Example for one component:

| MCK release | Component entries since previous pin | Component pinned version | Image tag published |
|---|---|---|---|
| 1.14.0 | first adoption | 1.14.0 | `proxy:1.14.0` |
| 1.14.1 | none | 1.14.0 | none |
| 1.15.0 | one fix | 1.15.0 | `proxy:1.15.0` |
| 1.15.1 | none | 1.15.0 | none |
| 1.16.0 | two entries | 1.16.0 | `proxy:1.16.0` |

## How adoption is derived

Computed in `update_release.py`, next to the existing `mongodbOperator` propagation, so it is deterministic and identical on every commit and on every `release-vN` branch:

```
prev_pin           = release.json[proxyPinnedVersion] at the last MCK tag on this branch
entries            = changelog/proxy/*.md added since the last MCK tag on this branch
adopt              = len(entries) > 0
proxyVersion       = mongodbOperator
proxyPinnedVersion = proxyVersion if adopt else prev_pin
```

- Entries are counted from the last MCK tag, the same base MCK uses for its own version. An entry cannot be held back, so anything older already adopted the component in the release that followed it.
- The pin is an MCK tag, so no component tags are needed.
- Bootstrap: with no previous pin, all entries count. The release that introduces the new architecture must be adopted.

## Changelog and release notes

- Layout: `changelog/<component>/<date>_<kind>_<title>.md`, same format as today. `create_changelog.py` gets `--component`.
- Validation (`check_changelog.py`): entries are allowed in a known component directory; an unknown subdirectory is an error, because a typo would mean the entry is never adopted. The general "a changelog entry is required" PR check stays as it is; there is no component-specific check.
- MCK version bump and release notes: component entries are counted and listed like any other entry. `version.py`, `release_notes.py` and the preview workflow need no change, because `changelog/` is already read recursively.

## Release pipeline

- Staging builds are unchanged: images are tagged with the short commit SHA regardless of release version, so e2e keeps testing HEAD code.
- `build_info.json` marks a component image with its pinned-version key (name to be decided, for example `pinned-version-ref: proxyPinnedVersion`) next to the existing `version-ref: proxyVersion`. `LoadReleaseImages` (used by both `release promote` and `release publish`) includes such an image only if the two `release.json` values are equal. Held components are neither promoted nor published. Images without the key behave as today.
- Publishing a component must never overwrite an existing tag. Today `PublishImages` passes `Force: true` and the only `operator` is in the default fail-on-stomp list.
- Signing, SBOM and Trivy scans work as for other images; they run only when a version is published.

## How customers receive component versions

All installation methods are generated from `release.json` at release time and hardcode the pinned tag. Customers do not choose or update a component version by hand: upgrading MCK means applying the newer chart, yaml or bundle, and the component tag changes only when the pin changed.

- Helm: `helm_chart/values.yaml` feeds the operator env vars in `helm_chart/templates/operator.yaml` (for example `READINESS_PROBE_IMAGE` today). `update_helm_values_files.py` writes the values from `release.json`; it will read the pinned fields.
- Custom yaml: `public/mongodb-kubernetes.yaml` and `config/manager/manager.yaml` are generated from the Helm template.
- OpenShift/OLM: the operator Deployment carries `RELATED_IMAGE_*` env vars, which `operator-sdk` turns into the bundle's `relatedImages` with digests for restricted networks. The pinned proxy and reporter tags need their own entries.

## Support

- Supported component version of an MCK release: the one its `release.json` pins. Fixes ship only in a new MCK release; customers upgrade the operator. Operator upgrade restarts Pods for a component only when its image version has changed.
- Backports follow the backporting policy (SPIKE: Support Backporting to Prior Major Release): security and critical fixes only, only to the latest minor of a previous major (`release-vN`), for one year after the next major. No backports to minor lines within a major.
- Backport flow: cherry-pick the fix and its changelog entry to `release-vN`. The hook derives the pin as that branch's next MCK version (for example 1.20.1), the release publishes `proxy:1.20.1`, and master is untouched. There is no clash, because MCK versions are unique across branches. This first matters at MCK 2.0, if the proxy ships in 1.x.
- Helm exposes `name` and `registry` (mirroring). The version is written into the chart at release; changing it is not supported. `MDB_PROXY_IMAGE` and `MDB_REPORTER_IMAGE` remain internal knobs (used by e2e) and are documented as unsupported for production.
- Release notes and docs state which component versions an MCK release uses (for example "mongodb-proxy 1.14.0").
- Compatibility: an operator pins only versions from its own release or earlier. A PR that changes the proxy API contract must carry a changelog entry for the component. A CI smoke job runs HEAD operator against the pinned component images.

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

Either way, adoption is changelog-driven in this TD, so a dependency or toolchain update adopts the component only when someone writes an entry. A separate `go.mod` would make an automatic, path-based check possible; that is out of scope here.

Revisit if: the proxy's dependency graph grows or diverges from the operator's, the reporter ends up with a small closure, or we want dependency updates to adopt components automatically.

## Alternatives considered and discarded

| Solution | Biggest cons |
|---|---|
| Lockstep: component tag equals MCK version and is always adopted | Every MCK upgrade changes the tag and restarts all database Pods, even with no component change. |
| Independent semver, manual bump (the readiness probe pattern) | Bumps are manual, so dependencies go stale. A backport and master can both need `1.4.1` with different contents, and the publish step can overwrite a released tag. |
| Independent semver, automated bump from changelog entries | Same backport clash; needs component tags and prefix-aware tag lookup. |
| Publish a component image on every release, pin separately | Publishes images no release uses (versions with no actual change); clutter in registries, mirror lists and scans. |
| Separate Go module per component (`proxy/vX.Y.Z` tags) | Gives dependency visibility, not a bump policy. Release tooling assumes one version tag, and the repository moved away from nested modules. See "Single go.mod or one per component". |
| Separate repository | The TD builds from the MCK repository; cross-repository e2e and release coordination. |
| Out-of-band component version override | Ruled out: all releases go through MCK. |

## Out of scope

- Holding component changes back to batch them: every component changelog entry adopts the component in the next MCK release, so the only control is when MCK is released.
- Blocking an MCK release on a reachable vulnerability in a pinned component (adopt or record a waiver).
- Scheduled scan of the pinned component images on every supported branch. Scans run today only when an image is built, so a held pin is not rescanned after release.
- Automatic adoption of dependency or toolchain updates: Dependabot-created entries, a dependency-closure check, and a component-specific "entry required" check. With a single `go.mod`, such updates adopt only when someone writes an entry.
- Generating release notes for the released commit. `release.yml` generates them on the dispatch branch HEAD, not on `commit_sha`. This is an existing issue, to be investigated separately; this sub-TD assumes the notes reflect the released commit.
- Out-of-band component version override.
- Migrating init containers, `readiness-probe` and `upgrade-hook` (1.0.24 and 1.0.10) into this scheme.

## Work items

1. Changelog model: add component directories. Parse and validate (known component directory, unknown subdirectory is an error) in `scripts/release/changelog.py` and `check_changelog.py`; add `--component` to `create_changelog.py`.
2. `release.json` fields and pin derivation: add `proxyVersion`, `proxyPinnedVersion`, `reporterVersion`, `reporterPinnedVersion`. Derive in `scripts/evergreen/release/update_release.py`; map the pinned fields to Helm in `update_helm_values_files.py`; read them in `release_info.py`.
3. Publish only when adopted, never overwrite an existing tag: pinned-version key in `build_info.json`, filter in `ci/internal/release/imageset.go`, no-overwrite in `images_publish.go`.
4. Installation configs: env vars and `RELATED_IMAGE_*` entries for proxy and reporter in `helm_chart/templates/operator.yaml`, values in `helm_chart/values.yaml` and `values-openshift.yaml`; regenerate `public/*.yaml` and `config/manager/manager.yaml`; check the generated OLM bundle.
5. Tests: scenarios for a held component, an adopted component, and a backport on a release branch. `scripts/release/tests/conftest.py`, `testdata/test_git_repo.mmd`.
6. Compatibility smoke job: HEAD operator against the pinned component images, in Evergreen.
7. Docs: state the component versions used by each MCK release in the release notes and public docs.

## Open questions

- Are gaps in published tags acceptable? The concern was versions without actual changes; here a version exists only when something changed.
- Names: `proxyPinnedVersion` in `release.json` and the pinned-version key in `build_info.json`.
- Helm `--reuse-values` keeps previous values for existing keys. If the pinned tag is a chart value, it may not move on `helm upgrade --reuse-values`. Do we document that flag, and should the template carry the pins outside user-overridable values?
