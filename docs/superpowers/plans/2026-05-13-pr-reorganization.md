# PR Stack Reorganization — MCO Decoupling

**Date:** 2026-05-13  
**Goal:** Collapse 10 PRs (base + 9) into 5 (base + 4) by merging all pure package/type moves and the auth cleanup into two thematic PRs.

---

## Current Stack

```
master
  decuple-mdb-community          (PR #1088 — base, kube+util moves)
    decuple-plan-2-automationconfig      (PR #1089)  tip: 9848788ec
      decuple-plan-3-api-common          (PR #1090)  tip: bfd33be7f
        decuple-plan-4-agent-mongot-tls  (PR #1091)  tip: 4a4177f52
          decuple-plan-6-inline-controllers-refs (PR #1098)  tip: f4bbfa647
            decuple-plan-7-construct     (PR #1099)  tip: 28ec12981
              decuple-plan-8-auth-and-audit (PR #1100)  tip: a74d719d6
                decuple-plan-9-api-common-types (PR #1104)  tip: 3dc62f5f7
                  decuple-plan-10a-appdb-construct-copy (PR #1105)  tip: f78b97184
                    decuple-plan-10b-appdb-construct-simplify (PR #1106)
```

## Target Stack

```
master
  decuple-mdb-community               (PR #1088 — closed/superseded)
    decuple-1-shared-packages          (new PR — replaces #1088, #1089, #1090, #1091, #1104)
      decuple-2-controllers-surface    (new PR — replaces #1098, #1099, #1100)
        decuple-3-appdb-copy           (new PR — replaces #1105)
          decuple-4-appdb-simplify     (new PR — replaces #1106)
```

**Grouping rationale:**
- **PR 1** — everything that *moved* without changing: kube/util packages (#1088), automationconfig/api/tls packages (Plans 2+3+4), api/v1 type dissolution (Plan 9). One PR, zero logic changes, reviewers just verify packages landed correctly.
- **PR 2** — everything that *cut the import surface*: inline MCO helpers into Enterprise (Plan 6), move constants out of MCO construct (Plan 7), auth packages + forbidigo (Plan 8).
- **PR 3** — copy AppDB StatefulSet builder into Enterprise (Plan 10a, pure copy, no logic change).
- **PR 4** — simplify both sides now that they're decoupled (Plan 10b).

---

## Verified Commit SHAs

### PR 1 cherry-picks (in dependency order: #1088 → 2 → 3 → 4 → 9)

**#1088 commits** (`decuple-mdb-community`, 10 commits):
```bash
git log --oneline origin/decuple-mdb-community ^master
```
Cherry-pick the full range:
```bash
git cherry-pick master..origin/decuple-mdb-community
```

**Plan 2:**
| SHA | Message |
|-----|---------|
| `9848788ec` | refactor: move pkg/automationconfig + 2 transitive deps from MCO to root |

**Plan 3:**
| SHA | Message |
|-----|---------|
| `bfd33be7f` | refactor: move api/v1/common from MCO to root |

**Plan 4:**
| SHA | Message |
|-----|---------|
| `4a4177f52` | refactor: move pkg/{agent,mongot,tls} from MCO to root |

**Plan 9:**
| SHA | Message |
|-----|---------|
| `6cc622848` | refactor(decouple): Plan 9 — move shared MCO spec types to api/v1/common |
| `d237bc557` | fixup(decouple): Plan 9 — move types from api/v1/common to api/v1 top level |
| `3dc62f5f7` | fixup(decouple): Plan 9 — standardize api/v1 import alias to v1 |

---

### PR 2 cherry-picks (in dependency order: Plan 6 → Plan 7 → Plan 8)

**Plan 6 commits** (3 commits):
| SHA | Message |
|-----|---------|
| `20be74afb` | refactor: inline OverrideToAutomationConfig + ListenAddress into Enterprise |
| `2e387b4ac` | refactor: inline community_helpers.go contents into appdbreplicaset_controller.go |
| `f4bbfa647` | fixup: re-add inlined ListenAddress + OverrideToAutomationConfig |

**Plan 7 commits** (9 commits, oldest-first):
| SHA | Message |
|-----|---------|
| `f596a6ec5` | refactor(decouple): Plan 7 — hybrid MOVE of shared constants to pkg/construct |
| `f1b58e904` | fixup(decouple): Plan 7 — drop pkgconstruct alias |
| `a39d130f7` | refactor(decouple): consolidate image URL env constants into pkg/images |
| `e96338ea7` | fixup(decouple): reuse pkg/images constants in MCO construct package |
| `cacfab97b` | fixup(decouple): remove duplicate OfficialMongodbEnterpriseServerImageName from MCO |
| `5a438fe60` | fixup(decouple): Plan 7 — restore util constants, rename *Repo→*Url |
| `f9aba6bba` | fixup(decouple): Plan 7 — remove duplicate constants from pkg/images |
| `dc22c3d90` | fixup(decouple): Plan 7 — move Url/Env constants to util |
| `28ec12981` | fixup(decouple): Plan 7 — move AgentImageEnv to pkg/util/constants |

**Plan 8 commits** (5 commits, tip `a74d719d6` after forbidigo fixup):
| SHA | Message |
|-----|---------|
| `dab1883f0` | refactor(decouple): Plan 8 — auth long tail + audit follow-ups |
| `ca61f0c38` | fixup(decouple): revert TC-2/TC-3 constant renames in pkg/util/constants |
| `a0bef709e` | fix: expand forbidigo rule to cover all pkg/util/env exports |
| `d9d565a9b` | fixup(decouple): Plan 8 — restore scram tests, fix forbidigo nolints, cleanup |
| `a74d719d6` | fixup(decouple): revert forbidigo expansion; remove env.ToMap/MergeWithOverride nolints |

---

### PR 3 own commits (Plan 10a, 6 commits, tip `f78b97184`)
| SHA | Message |
|-----|---------|
| `4da520aba` | feat(decouple): add MongodbContainerName constant to pkg/util |
| `9bef90c28` | feat(decouple): copy MCO agent command helpers into Enterprise construct package |
| `b01781710` | feat(decouple): copy MCO StatefulSet builder and AppDBStatefulSetOwner interface into Enterprise construct |
| `4afdeff94` | feat(decouple): wire appdb_construction.go to use local StatefulSet builder (drop MCO construct import) |
| `1ba9a1b38` | fix(decouple): replace result.OK() with r.updateStatus() in AppDB skip path; drop result import |
| `f78b97184` | fixup(decouple): fix v1 import alias conflict in appdb_construction_test.go (Plan 9 regression) |

Note: `f78b97184` may become a no-op once Plan 9 commits are ancestors of PR 3 (they fix the same alias conflict). If `git cherry-pick --skip` or `git rebase --skip` drops it cleanly, that is correct behaviour.

---

### PR 4 own commits (Plan 10b — branch `decuple-plan-10b-appdb-construct-simplify`)
Retrieve after 10b lands:
```bash
git log --oneline origin/decuple-plan-10a-appdb-construct-copy..origin/decuple-plan-10b-appdb-construct-simplify
```

---

## Conflict Risk Analysis

**PR 1 — Plans 2/3/4 + #1088 + Plan 9:**
- #1088, Plans 2/3/4: all non-overlapping file sets. Risk: **none**.
- Plan 9 builds on Plan 3's `api/v1/common` landing — applies correctly since Plan 3 is cherry-picked first. Risk: **low**.
- Plan 9's alias standardization (`3dc62f5f7`) touches ~18 files. No overlap with Plans 2/3/4 or #1088 directories.

**PR 2 — Plans 6/7/8:**
- Plans 6→7 chain is unchanged from original analysis. Risk: **low**.
- Plan 8 was originally on top of Plan 7's tip; the file states will match after Plan 7 cherry-picks land. Risk: **low**.
- Watch `controllers/operator/appdbreplicaset_controller.go`: touched by Plan 6 (function bodies) and Plan 7 (import block). If a conflict fires, keep Plan 7's import block plus Plan 6's function bodies.

**PR 3 rebase — Plan 10a onto PR 2 tip:**
- Plan 10a was originally on top of Plan 9. After reorg Plan 9 is in PR 1 (ancestor of PR 2). Plan 10a must be rebased onto PR 2's tip (which now includes Plan 8 rather than plan 8 being a separate PR on top).
- The Plan 9 regression fix (`f78b97184`) may become empty — drop with `git rebase --skip`.

**PR 4 — Plan 10b stacked on PR 3:** follows automatically.

---

## Step-by-Step Commands

### Pre-flight

```bash
git fetch origin
git log --oneline origin/decuple-mdb-community ^master | wc -l  # expect 10 commits
git log --oneline origin/decuple-plan-8-auth-and-audit | head -1  # expect a74d719d6
git log --oneline origin/decuple-plan-9-api-common-types | head -1  # expect 3dc62f5f7
git log --oneline origin/decuple-plan-10a-appdb-construct-copy | head -1  # expect f78b97184
```

---

### Step 1 — Create PR 1 branch

```bash
git checkout master
git checkout -b decuple-1-shared-packages

# Cherry-pick #1088 commits (kube/util moves)
git cherry-pick master..origin/decuple-mdb-community

# Cherry-pick Plans 2, 3, 4
git cherry-pick 9848788ec   # Plan 2
git cherry-pick bfd33be7f   # Plan 3
git cherry-pick 4a4177f52   # Plan 4

# Cherry-pick Plan 9 (builds on Plan 3's api/v1/common)
git cherry-pick 6cc622848
git cherry-pick d237bc557
git cherry-pick 3dc62f5f7

go build ./...
```

Expected: 14 clean cherry-picks, `go build` clean.

**Do NOT push yet.**

---

### Step 2 — Create PR 2 branch

```bash
git checkout decuple-1-shared-packages
git checkout -b decuple-2-controllers-surface

# Cherry-pick Plan 6 (3 commits)
git cherry-pick 20be74afb
git cherry-pick 2e387b4ac
git cherry-pick f4bbfa647

# Cherry-pick Plan 7 (9 commits)
git cherry-pick f596a6ec5 f1b58e904 a39d130f7 e96338ea7 cacfab97b 5a438fe60 f9aba6bba dc22c3d90 28ec12981

# Cherry-pick Plan 8 (5 commits including the forbidigo revert fixup)
git cherry-pick dab1883f0 ca61f0c38 a0bef709e d9d565a9b a74d719d6

go build ./...
```

**If conflict on `controllers/operator/appdbreplicaset_controller.go`:** keep Plan 7's import block (no `mcoConstruct`, yes `pkgconstruct`) and Plan 6's inlined function bodies. Run `git cherry-pick --continue`.

**Do NOT push yet.**

---

### Step 3 — Rebase Plan 10a onto PR 2

Plan 10a's current upstream is `decuple-plan-9-api-common-types`. Rebase it onto the new PR 2 tip.

```bash
git checkout decuple-plan-10a-appdb-construct-copy
git rebase --onto decuple-2-controllers-surface decuple-plan-9-api-common-types decuple-plan-10a-appdb-construct-copy
```

If `f78b97184` (Plan 9 regression fix) becomes empty (Plan 9 is now an ancestor), drop it:
```bash
git rebase --skip
```

```bash
go build ./...
```

Rename the branch to match the new scheme:
```bash
git branch -m decuple-plan-10a-appdb-construct-copy decuple-3-appdb-copy
```

---

### Step 4 — Rebase Plan 10b onto new Plan 10a tip

```bash
git checkout decuple-plan-10b-appdb-construct-simplify
git rebase --onto decuple-3-appdb-copy decuple-plan-10a-appdb-construct-copy decuple-plan-10b-appdb-construct-simplify
git branch -m decuple-plan-10b-appdb-construct-simplify decuple-4-appdb-simplify

go build ./...
```

---

### Step 5 — Update git machete file

```bash
cat > .git/machete <<'EOF'
master
  auto-bump-mongodb-operator-version
  decuple-1-shared-packages
    decuple-2-controllers-surface
      decuple-3-appdb-copy
        decuple-4-appdb-simplify
  maciejk/attack-ea
  maciejk/pss-warn
  maciejk/separate-tls-certs
  maciejk/tls-cert-investigation
maciejk/cert-manager
EOF

git machete status
```

Note: `decuple-mdb-community` is removed from machete — it will be closed as superseded.

---

### Step 6 — Full build + test gate

```bash
git checkout decuple-4-appdb-simplify
go build ./...
go test ./controllers/operator/... ./mongodb-community-operator/controllers/... -count=1 2>&1 | grep -E "FAIL|ok"
```

---

### Step 7 — Push and manage PRs

**Only execute after explicit approval.**

```bash
git push -u origin decuple-1-shared-packages
git push -u origin decuple-2-controllers-surface
git push --force-with-lease origin decuple-3-appdb-copy
git push --force-with-lease origin decuple-4-appdb-simplify
```

**Close superseded PRs:**

```bash
for pr in 1088 1089 1090 1091 1098 1099 1100 1104 1105 1106; do
  gh pr close $pr --comment "Superseded by reorganized MCO decoupling stack (decuple-1-shared-packages … decuple-4-appdb-simplify)."
done
```

**Open new PRs:**

```bash
gh pr create \
  --base master \
  --head decuple-1-shared-packages \
  --draft --label skip-changelog \
  --title "refactor(decouple): move shared packages and types from MCO to root" \
  --body "$(cat <<'EOF'
## Summary

All pure mechanical moves — zero logic changes. Combines:

- **#1088 (Plans 1):** Move shared kube and util leaf packages from MCO to root
- **Plan 2:** Move \`pkg/automationconfig\`, \`pkg/authentication/scramcredentials\`, \`pkg/util/versions\`
- **Plan 3:** Move \`api/v1/common\` from MCO to root
- **Plan 4:** Move \`pkg/{agent,mongot,tls}\`
- **Plan 9:** Dissolve \`api/v1/common\` sub-package — promote types to \`api/v1/\` top level; standardize \`api/v1\` import alias

## Test plan
- [ ] \`go build ./...\` passes
- [ ] No remaining imports of moved packages from their old MCO paths
EOF
)"

gh pr create \
  --base decuple-1-shared-packages \
  --head decuple-2-controllers-surface \
  --draft --label skip-changelog \
  --title "refactor(decouple): cut MCO controllers/construct and auth import surface" \
  --body "$(cat <<'EOF'
## Summary

Removes remaining MCO-specific cross-module imports from Enterprise code. Combines:

- **Plan 6:** Inline \`OverrideToAutomationConfig\` and \`ListenAddress\` from MCO into \`appdbreplicaset_controller.go\`
- **Plan 7:** Move shared image/container constants from MCO \`controllers/construct\` to \`pkg/images\` and \`pkg/util/constants\`
- **Plan 8:** Move auth packages; remove MCO auth imports; keep forbidigo pattern at \`env.(Read.*?|EnsureVar)\`

## Test plan
- [ ] \`go build ./...\` passes
- [ ] No import of \`mongodb-community-operator/controllers/construct\` outside MCO
- [ ] \`make precommit\` passes (forbidigo rules)
EOF
)"

gh pr create \
  --base decuple-2-controllers-surface \
  --head decuple-3-appdb-copy \
  --draft --label skip-changelog \
  --title "feat(decouple): copy AppDB StatefulSet builder from MCO into Enterprise" \
  --body "$(cat <<'EOF'
## Summary

Pure copy of MCO's StatefulSet builder into Enterprise \`controllers/operator/construct/\`. Zero logic changes — the follow-up PR (Plan 10b) simplifies signatures.

- Copies \`BuildMongoDBReplicaSetStatefulSetModificationFunction\`, agent command helpers, and all private helpers
- Adds \`MongodbContainerName\` constant to \`pkg/util/constants.go\`
- Drops \`mongodb-community-operator/controllers/construct\` import from AppDB code

## Test plan
- [ ] \`go build ./...\` passes
- [ ] No import of \`mongodb-community-operator/controllers/construct\` outside MCO
EOF
)"

gh pr create \
  --base decuple-3-appdb-copy \
  --head decuple-4-appdb-simplify \
  --draft --label skip-changelog \
  --title "refactor(decouple): simplify AppDB and MCO StatefulSet builders" \
  --body "$(cat <<'EOF'
## Summary

Now that AppDB owns its builder copy, remove dead params from both sides.

**Enterprise:** Remove \`versionUpgradeHookImage\`/\`readinessProbeImage\` (always \`""\`) and \`withAgentAPIKeyExport\` (always \`true\`).

**MCO:** Remove \`withInitContainers\`/\`initAppDBImage\` (always \`true\`/\`""\`), remove \`withAgentAPIKeyExport\` (always \`false\`).

Note: \`MongoDBAssumeEnterpriseEnv\` was kept in MCO — it is still referenced in \`replica_set_controller.go\`.

## Test plan
- [ ] \`go build ./...\` passes
- [ ] Unit tests pass for both construct packages
- [ ] \`make precommit\` passes
EOF
)"
```

**Update PR chain descriptions:**

```bash
git machete github update-pr-descriptions --all
```

---

## Post-reorganization Stack Summary

| # | Branch | Base | Content |
|---|--------|------|---------|
| 1 | `decuple-1-shared-packages` | `master` | All package/type moves (#1088 + Plans 2+3+4+9) |
| 2 | `decuple-2-controllers-surface` | PR 1 | Import surface cuts (Plans 6+7+8) |
| 3 | `decuple-3-appdb-copy` | PR 2 | AppDB StatefulSet builder copy (Plan 10a) |
| 4 | `decuple-4-appdb-simplify` | PR 3 | Signature simplification (Plan 10b) |

4 PRs above master — down from 9.

---

## Rollback

```bash
git branch -D decuple-1-shared-packages decuple-2-controllers-surface

git branch -m decuple-3-appdb-copy decuple-plan-10a-appdb-construct-copy
git branch -m decuple-4-appdb-simplify decuple-plan-10b-appdb-construct-simplify

git checkout decuple-plan-10a-appdb-construct-copy && git reset --hard f78b97184
git checkout decuple-plan-10b-appdb-construct-simplify && git reset --hard origin/decuple-plan-10b-appdb-construct-simplify

cat > .git/machete <<'EOF'
master
  auto-bump-mongodb-operator-version
  decuple-mdb-community
    decuple-plan-2-automationconfig
      decuple-plan-3-api-common
        decuple-plan-4-agent-mongot-tls
          decuple-plan-6-inline-controllers-refs
            decuple-plan-7-construct
              decuple-plan-8-auth-and-audit
                decuple-plan-9-api-common-types
                  decuple-plan-10a-appdb-construct-copy
                    decuple-plan-10b-appdb-construct-simplify
  maciejk/attack-ea
  maciejk/pss-warn
  maciejk/separate-tls-certs
  maciejk/tls-cert-investigation
maciejk/cert-manager
EOF
```
