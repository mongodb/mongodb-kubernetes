# Dry-run releases

A dry-run runs the full `release_publish` pipeline (`.evergreen-release-publish.yml`) with most destinations
redirected to staging, but is **not side-effect-free**: images and the Helm chart are published to the `REGISTRY` override
(staging by default), while the kubectl plugin is always promoted into the production S3 bucket `mongodb-kubernetes-release`
(independent of `BUILD_SCENARIO`) and chart/RH fork branches are pushed.

Patch params:

- `BUILD_SCENARIO=dryrun-release` — selects release build info/dry-run behavior. Red Hat preflight checks still run, but certification submission is off by default (opt in with `--param preflight_submit=true`); the Helm chart PR is skipped.
- `triggered_by_git_tag` — simulates the tag name without a real tag.
- `target_commit` — required (no git tag exists); resolves the release commit and S3 staging paths.
- `tag_description_override` — synthetic tag annotation; `[new]` prefix selects the publish-only pipeline.
- Optional: `--param REGISTRY=quay.io/<personal-org>` redirects publication elsewhere.

```shell
evergreen patch -p mongodb-kubernetes -v release_decider_patch -t decide_release_process \
  -d "Dry-run release X.Y.Z" -f -y -u \
  --param BUILD_SCENARIO=dryrun-release --param triggered_by_git_tag=X.Y.Z \
  --param target_commit=<full-sha> --param tag_description_override="[new] Dry-run X.Y.Z"
```

| Step | Real release | Dry-run |
|---|---|---|
| Images / chart destination | `quay.io/mongodb` | `REGISTRY` (staging org by default) |
| Kubectl plugin destination | `mongodb-kubernetes-release` S3 | **same prod bucket** — always promoted, not redirected |
| Trivy CVE scan | prod image refs | overridden registry refs (still runs) |
| Red Hat certification submission | submitted | off by default (`--param preflight_submit=true` to opt in) |
| GitHub release | real `X.Y.Z` tag + assets | draft under `test-X.Y.Z` (auto-created), plugin assets attached there |
| Helm chart PR | branch + PR on `mongodb/helm-charts` | branch only, no PR |
| OpenShift bundles → RH forks | branch pushed, PRs manual | unchanged (forks only, force-push) |

Cleanup: delete the `test-X.Y.Z` draft release when done; `mck-release-X.Y.Z` is force-pushed by the real release, staging artifacts are overwritten by the next run, and the plugin tarballs promoted to `mongodb-kubernetes-release` for a dry-run version must be removed manually.

## GitHub Actions dry-run (`do_not_tag=true`)

Separate from Evergreen `BUILD_SCENARIO`: dispatch `Release MCK version (publish-only)` (`.github/workflows/release.yml`) with the 40-char `commit_sha`, `version`, and `do_not_tag=true`; it skips draft release/tag creation, git tag/push, and publishing.
