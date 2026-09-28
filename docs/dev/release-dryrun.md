# Dry-run releases

A dry-run executes the full `release_publish` pipeline (`.evergreen-release-publish.yml`)
without touching production: images, the helm chart and the kubectl plugin are published
to `quay.io/mongodb/staging`, no GitHub release or PR is created for real, and nothing is
submitted to Red Hat certification. Use it to validate a release end-to-end before
pushing the tag.

## How it works

Dry-run is not a code path — it is the regular release pipeline with two patch params:

- `BUILD_SCENARIO=dryrun-release` — the single signal. Scripts key off it (via
  `BuildScenario.DRYRUN_RELEASE`, which reads the `release` build info) to redirect the
  destination and skip prod-only side effects.
- `REGISTRY` — destination override. Defaults to `quay.io/mongodb/staging` in
  `scripts/dev/contexts/evg-private-context`; set it to publish somewhere else (e.g. a
  personal Quay org).

`target_commit` is required because patches have no git tag; the pipeline resolves the
release commit and the S3 staging paths from it. `preflight_submit=false` skips the Red
Hat certification submission.

## Kicking a dry-run

To test without even creating a tag in GitHub:

```shell
evergreen patch -p mongodb-kubernetes -v release_decider_patch -t decide_release_process \
  -d "Dry-run release X.Y.Z" -f -y -u \
  --param BUILD_SCENARIO=dryrun-release --param triggered_by_git_tag=X.Y.Z \
  --param target_commit=<full-sha> --param preflight_submit=false \
  --param tag_description_override="[new] Dry-run X.Y.Z"
```

Optional: add `--param REGISTRY=quay.io/<personal-org>` to redirect publication elsewhere.

Use `tag_description_override="[new] ..."` only if the tag is not in git yet — it drives the
`[new]` pipeline selection in `decide_release_process`.

The decider generates the `release-publish` variants; patch filtering accepts the
`release-publish` tag.

## What differs from a real release

| Step | Real release | Dry-run |
|---|---|---|
| Images / chart / plugin | `quay.io/mongodb` | `REGISTRY` (staging org by default) |
| Trivy CVE scan | prod image refs | overridden registry refs (still runs) |
| Red Hat preflight | submitted (`preflight_submit: true`) | `--submit false` via patch param |
| GitHub release | real `X.Y.Z` tag, assets attached | draft under `test-X.Y.Z` (auto-created) |
| Helm chart PR | branch + PR on `mongodb/helm-charts` | branch only, no PR |
| OpenShift bundles → RH forks | branch pushed, PRs manual | unchanged (forks only, force-push) |

## Cleanup

- Delete the `test-X.Y.Z` draft GitHub release when done validating.
- The `mck-release-X.Y.Z` branch on `mongodb/helm-charts` is overwritten by the real
  release (force-push) — no cleanup needed.
- Staging images/chart are overwritten by the next run of the same version.

## Real release as a patch (hotfix/re-run)

Same invocation with `BUILD_SCENARIO=release` instead, no `preflight_submit` override —
this publishes to production from a patch, bypassing the tag trigger:

```shell
evergreen patch -p mongodb-kubernetes -v release_decider_patch -t decide_release_process \
  -d "Release version X.Y.Z" -f -y -u \
  --param BUILD_SCENARIO=release --param triggered_by_git_tag=X.Y.Z \
  --param target_commit=<full-sha> --param tag_description_override="[new] Release of MCK X.Y.Z"
```

