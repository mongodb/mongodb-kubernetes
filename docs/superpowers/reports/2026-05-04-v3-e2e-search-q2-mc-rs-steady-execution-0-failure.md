# v3 G2 — `e2e_search_q2_mc_rs_steady` execution-0 failure analysis

## Patch + task

- Patch v3: <https://evergreen.mongodb.com/version/69f84d562588c300076df0f7>
  ("Phase 2 G2 v3: + MDB version pin fix (q2_mc_rs_steady)", created
  2026-05-04 07:40:08 UTC)
- Failed task: <https://spruce.corp.mongodb.com/task/mongodb_kubernetes_e2e_static_multi_cluster_2_clusters_e2e_search_q2_mc_rs_steady_patch_891db34491348134de423d70b9df9b46d6ffa246_69f84d562588c300076df0f7_26_05_04_07_40_09/logs?execution=0>
- Test: `tests.multicluster_search.q2_mc_rs_steady.test_create_mdb_resource`
- Duration: 1501.669s (the timeout was 1500)

## Category

**Submission error — stale patch snapshot.** The `mc-search-phase2-q2-rs`
branch on GitHub already contains the version-pin fix (PR #1055,
commit `eebd61b6e`, merged 2026-05-04 07:38:36 UTC) plus five other
post-`2842cf745` commits, but the v3 patch payload was generated from
the stale tip `2842cf745` and therefore did not include any of those
commits. The fix is correct in code; it was just never delivered to
Evergreen for v3.

This does not match any of the prompt's enum values cleanly. It is
not a "Real bug" (the fix is merged), not a "Test setup gap" (the
test code on the branch is correct), not "Infra flake", not "Race".
The closest existing label is "Unknown"; in practice it should be
treated as a **patch-submission process error**.

## Failure shape (identical symptom to v2 ex 0)

The pytest summary, the controlling log, and the operator-side state
match v2 ex 0 line for line:

```
FAILED tests/multicluster_search/q2_mc_rs_steady.py::test_create_mdb_resource
  - Exception: Timeout (1500) reached while waiting for MongoDB (mdb-mc-rs-ext-lb)
    | status: Phase.Pending| message: StatefulSet not ready
```

(`kind-e2e-cluster-1__a-1777881310-u9390i6g7tz_mongodb-enterprise-operator-tests-mongodb-enterprise-operator-tests.log`,
line 2585)

The MongoDBMulti is created and stays at `Phase.Pending` /
`message: StatefulSet not ready` for the full 25-minute wait, with
the spec showing **`"version":"6.0.5-ent"`** — exactly the v2 root
cause:

```
"spec":{ ... "type":"ReplicaSet","version":"6.0.5-ent" }
```

(same log, line 1190 onward, every 3-second poll for the duration of
the wait)

## Smoking gun: the v3 patch payload does NOT carry the version pin

Confirmed by reading the v3 patch's raw diff
(`https://evergreen.mongodb.com/rest/v2/patches/69f84d562588c300076df0f7/raw`,
saved locally to `/tmp/v3-patch/v3.diff`):

- `docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml`:
  - Patch creates this file as **18 lines / 0 deletions**, with **no
    `version: 8.2.0-ent`** line. (raw diff lines 8795-8813)
  - The PR #1055 fix would have made it 21 lines and added the
    pin near `type: ReplicaSet`.
  - The branch HEAD `eebd61b6e` (verified via
    `git show eebd61b6e:.../search-q2-mc-rs.yaml`) HAS the pin.
- `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`:
  - Patch creates this file as **668 additions / 0 deletions**.
  - Branch HEAD `eebd61b6e` is 743 lines.
  - 668-line revision matches commit `2842cf745` exactly
    (`git show 2842cf745:...q2_mc_rs_steady.py | wc -l` → 668).
  - The `mdb` fixture in the patch payload (raw diff line 8957-8978)
    still parameterizes on `custom_mdb_version: str` and calls
    `resource.set_version(ensure_ent_version(custom_mdb_version))` —
    both removed by PR #1055.
- The pytest setup line itself confirms it at runtime:
  `SETUP    M mdb (fixtures used: ca_configmap, central_cluster_client,
  custom_mdb_version, member_cluster_names, namespace)` (test log
  line 185). On the branch HEAD, `custom_mdb_version` would not be in
  that list.

So the v3 patch is a snapshot of the worktree at commit `2842cf745`,
not at branch HEAD `eebd61b6e`. The version-pin fix (and five other
commits) were missing from the payload uploaded to Evergreen.

## Comparison to v2 ex 0

| Dimension                         | v2 ex 0                                     | v3 ex 0                                     |
| --------------------------------- | ------------------------------------------- | ------------------------------------------- |
| Test that failed                  | `test_create_mdb_resource`                  | `test_create_mdb_resource`                  |
| Failure mode                      | Timeout 1500s, Phase.Pending, SS not ready  | Timeout 1500s, Phase.Pending, SS not ready  |
| Effective MDB version on cluster  | `6.0.5-ent`                                 | `6.0.5-ent`                                 |
| Mongod logs (smoking gun in v2)   | "BadValue: Unknown --setParameter 'searchIndexManagementHostAndPort'" | (not re-verified for v3 — same setup, same result) |
| `set_version(ensure_ent_version(custom_mdb_version))` in test code? | Yes (#1054 root cause) | **Yes — the patch payload still has it, even though the fix is merged on the branch** |
| Branch HEAD has the fix?          | n/a (fix authored after v2)                 | Yes — at `eebd61b6e` (PR #1055 merged 07:38:36 UTC, ~90s before v3 was created at 07:40:08 UTC) |
| Why it failed the same way        | Fix didn't exist yet                        | Fix exists on branch, but not in patch payload |

The fix from PR #1055 is verified-correct from a code-reading
standpoint:

- Branch HEAD pins `version: 8.2.0-ent` in the fixture YAML and
  removes both the `custom_mdb_version` fixture parameter and the
  `set_version(ensure_ent_version(...))` call (commit `eebd61b6e`).
- `MongoDB.from_yaml` (`docker/mongodb-kubernetes-tests/kubetester/mongodb.py:54-60`)
  only overrides the resource version if
  `semver.compare(resource.get_version(), CUSTOM_MDB_VERSION) < 0`.
  Verified `semver.compare("8.2.0-ent", "6.0.5") = 1` (positive,
  i.e. 8.2.0-ent > 6.0.5), so with the fixture pinned to `8.2.0-ent`
  the override does NOT trigger.

The fix's behavior is therefore unverified empirically only because
no Evergreen run has yet executed the post-#1055 code.

## What else is missing from v3

`git log --oneline 2842cf745..eebd61b6e` shows the v3 patch is
missing **6 commits**:

```
eebd61b6e fix(test): pin q2_mc_rs_steady source mongod to 8.2.0-ent (#1055)
94c1c94a8 simplify: holistic Phase 2 cross-commit cleanup
0884aee14 test(search): MC RS — patch per-cluster mongotHost via Ops Manager AC after MongoDBMulti is Running
2da450cdb test(search-envoy): tidy up extra blank line between regression tests
98298f944 docs(report): Phase 2 G2 morning hand-off — blocker fixed, OAuth-gated submit
90d9cad2a fix(search-envoy): per-cluster Envoy pod mounts the per-cluster ConfigMap volume
```

`git diff 2842cf745 eebd61b6e --stat` shows real operator-side code
changes too: `controllers/operator/mongodbsearchenvoy_controller.go`
(±51 lines), `mongodbsearchenvoy_controller_test.go` (±86),
`mongodbsearch_reconcile_full_mc_test.go` (±59), api/v1/search
type and validation tweaks, plus the per-cluster mongotHost AC patch
test step in `q2_mc_rs_steady.py` (±169). So v3 is testing a
substantially older codebase than the branch HEAD — even if the test
had not failed at the version-pin stage, downstream assertions could
have surfaced bugs that are already fixed on `eebd61b6e`.

## Recommended next step

**Re-submit the patch as v4 from a worktree whose HEAD is
`eebd61b6e` or later.** Pre-submission sanity checks:

```bash
# 1. Branch tip matches the merged PR #1055 commit.
git rev-parse HEAD                    # should be eebd61b6e or a descendant
git log --oneline | head -5

# 2. Fixture YAML carries the pin.
git show HEAD:docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml \
  | grep '8\.2\.0-ent'                # must print the version line

# 3. Working tree is clean of unrelated staged/uncommitted changes
#    (otherwise `evergreen patch -u` will pick them up).
git status -uno
```

The user pre-authorization for `evergreen patch` runs of the active
MC Search Base + Phase 2 plan applies, but submission tooling
(`scripts/submit-phase-2-g2-patch.sh` on the branch) should be run
after `git pull` to pick up the post-#1055 tip; the prior submission
ran from a stale local worktree.

There is no code-side change to push for this analysis. The
`recommended-next` is purely a process correction.

## Artifacts

- Local task artifact dir:
  `/Users/anand.singh/.claude/plugins/cache/core-platforms-ai-tools/mck-dev/0.3.8/tmp/evergreen_artifacts/69f84d562588c300076df0f7/e2e_static_mult__2_clusters__e2e_search_q2_mc_rs_steady__ex0/`
- v3 raw patch diff (saved during analysis):
  `/tmp/v3-patch/v3.diff`
- Prior v2 root-cause writeup (PR #1054): same dir,
  `2026-05-04-v2-e2e-search-q2-mc-rs-steady-execution-0-failure.md`
- PR with the actual fix: <https://github.com/mongodb/mongodb-kubernetes/pull/1055>
  (merged at `eebd61b6e`).
