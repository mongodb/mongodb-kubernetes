# Work items

Subtab of [Engineering Proposal: Versioning and release of MCK components](td-versioning-release-support.md).

1. Changelog model: add component directories. Parse and validate (known component directory, unknown subdirectory is an error) in `scripts/release/changelog.py` and `check_changelog.py`.
2. Changelog tooling: `create_changelog.py` gets a `--component <name>` flag and creates the entry in `changelog/<component>/`. It is also the way to bump a component manually.
3. Component changelog check: a CI check with a path map per component, failing a component code change without an entry (`skip-changelog` as the escape). Includes the allowlisted shared packages of each component.
4. depguard rules per component in `.golangci.yml`, each with its allowlist.
5. `release.json` fields and minimum derivation: add a latest and a minimum field per component. Derive in `scripts/evergreen/release/update_release.py`; map them to Helm in `update_helm_values_files.py`; read them in `release_info.py`.
6. Migrate the existing components. Readiness probe and version upgrade hook: replace the hand-edited `readinessProbeVersion` and `versionUpgradeHookVersion` with the latest and minimum fields, seeding the minimum from 1.0.24 and 1.0.10, move their `version-ref` in `build_info.json`, and drop the manual bump steps. Init images: keep `initDatabaseVersion` and `initOpsManagerVersion` as the latest fields, add the minimum fields seeded from the last released version. For all four: add component directories, path maps and depguard allowlists. The `init-database` path map includes the readiness probe and version upgrade hook code it embeds.
7. Checklist for adding a new component to the build and release pipeline: `build_info.json`, `build_info.py`, `pipeline.py`, `pipeline.sh`, Evergreen build and `cve_scan_images` tasks, `evg-private-context`, the Makefile and `update_go_version.sh`.
8. Operator: version-based env vars per component, the global track-latest switch, and image resolution through `ContainerImage()`. `main.go`, `pkg/images`.
9. Installation configs: env vars and values in `helm_chart/templates/operator.yaml`, `values.yaml` and `values-openshift.yaml`; `RELATED_IMAGE_*` entries for both minimum and latest per component; the yaml generated with track latest on; regenerate `public/*.yaml` and `config/manager/manager.yaml`; check the generated OLM bundle.
10. Tests: derivation scenarios (no entry keeps the minimum, an entry moves it, a backport on a release branch, a component `feature` bumps the MCK version by a minor and a component `breaking` by a major) in `scripts/release/tests/conftest.py` and `testdata/test_git_repo.mmd`; operator unit tests for version resolution.
11. Smoke test gate in Evergreen on merges to master: the operator at HEAD against the minimum component images.
12. Docs: state the minimum component versions in each MCK release's notes and public docs, and document track latest and manual versions per installation method.
13. Add the new env vars to the Installation Unification env-var migration inventory.
