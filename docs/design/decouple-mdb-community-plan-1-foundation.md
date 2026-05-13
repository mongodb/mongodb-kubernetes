# Plan 1 — Foundation: move shared `pkg/kube/*` + `pkg/util/*` packages to root

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Relocate 18 shared utility packages out of `mongodb-community-operator/pkg/` to root `pkg/` so Enterprise can import them without crossing the MCO boundary, and resolve the `pkg/kube/service` name collision in the process. Pure refactor — zero behavioural change.

**Architecture:** One package move at a time, in topological-dependency order so the build stays green at every commit. Each move is: `git mv` files → rewrite import paths repo-wide with `goimports`/`sed` → build → test → commit. The `pkg/kube/service` collision is resolved first (separate commit) since both sides currently exist at the colliding path.

**Tech Stack:** Go 1.25, `go build`, `go test`, `goimports`, `gofmt`, `make lint`, `git mv`, `golangci-lint` (existing in repo).

**Spec reference:** `docs/design/decouple-mdb-community-investigation.md` (cluster 1 + cluster 5).

**Final move scope (18 packages):**
- **kube (12):** `annotations, client, configmap, container, lifecycle, persistentvolumeclaim, pod, podtemplatespec, probes, resourcerequirements, secret, service`
- **util (6):** `apierrors, constants, contains, envvar, merge, scale`

> `util/contains` is not in the spec's initial scope but is a transitive dependency of `kube/secret` and `util/merge`. Without it, those moves create new outside→MCO violations. Included.

**Topological move order** (sources first, so each commit leaves the build green):
1. `pkg/kube/service` — collision resolution (separate, special)
2. Pure leaves (no MCO subpackage deps): `util/constants`, `util/envvar`, `util/apierrors`, `util/scale`, `kube/annotations`, `kube/configmap`, `kube/lifecycle`, `kube/pod`, `kube/probes`, `kube/resourcerequirements`, `kube/persistentvolumeclaim`
3. `util/contains` (depends on `util/constants`)
4. `kube/secret` (depends on `util/contains`)
5. `kube/container` (depends on `kube/{lifecycle,probes,resourcerequirements}`, `util/envvar`)
6. `kube/client` (depends on `kube/{configmap,pod,secret,service}`)
7. `util/merge` (depends on `kube/{container,probes}`, `util/contains`)
8. `kube/podtemplatespec` (depends on `kube/container`, `util/envvar`, `util/merge`)

---

## Chunk 1: Pre-flight + `pkg/kube/service` collision resolution

This chunk gets the working tree to a known-good baseline and resolves the one structural blocker (`pkg/kube/service` exists at both paths today). No package moves yet — those start in chunk 2.

### Task 1: Verify baseline build is green and capture pre-move snapshot

**Files:**
- Read: `pkg/kube/service/`, `mongodb-community-operator/pkg/kube/service/`
- Create: `/tmp/decuple-baseline.txt` (scratch, not committed)

- [ ] **Step 1: Confirm you are on the right branch**

```bash
git rev-parse --abbrev-ref HEAD
```

Expected: `decuple-mdb-community`. If not, run `git checkout decuple-mdb-community` first.

- [ ] **Step 2: Confirm the working tree is clean apart from the spec doc**

```bash
git status --short
```

Expected: at most `?? docs/design/` (the uncommitted spec + this plan) and a few `?? .idea/` IDE files. **No unstaged `M` changes to anything under `mongodb-community-operator/`, `pkg/`, `controllers/`, `api/`, or `cmd/`.** If you see Go-file changes, stash or revert before proceeding.

- [ ] **Step 3: Build the full module to confirm baseline green**

Run:

```bash
go build ./...
```

Expected: exit 0, no output.

If this fails, fix the failure before continuing — Plan 1 must start from green.

- [ ] **Step 4: Run unit tests (short mode) to capture baseline**

Run:

```bash
go test -short -count=1 ./... 2>&1 | tee /tmp/decuple-baseline.txt | tail -20
```

Expected: all `ok` / `PASS`, no `FAIL`. Save the file — it will be diffed against post-move runs to confirm zero behaviour change.

Note: full e2e suite is out of scope here; this plan only verifies unit tests + build. The e2e gate is the user's call when Plan 1 lands.

- [ ] **Step 5: Capture the inventory of MCO packages we are about to move**

Run:

```bash
{
  echo "--- MCO subpackages being moved ---"
  for p in annotations client configmap container lifecycle persistentvolumeclaim pod podtemplatespec probes resourcerequirements secret service; do
    echo "kube/$p: $(ls mongodb-community-operator/pkg/kube/$p/*.go 2>/dev/null | wc -l) files"
  done
  for p in apierrors constants contains envvar merge scale; do
    echo "util/$p: $(ls mongodb-community-operator/pkg/util/$p/*.go 2>/dev/null | wc -l) files"
  done
  echo
  echo "--- Total importers outside MCO of these specific subpackages ---"
  grep -rln --include='*.go' '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/\(kube\|util\)' . 2>/dev/null \
    | grep -v '^./mongodb-community-operator/' \
    | grep -v '^./vendor/' \
    | wc -l
} >> /tmp/decuple-baseline.txt
```

Expected: every `kube/*` shows 1–3 files, every `util/*` shows 1–2 files, and the importer count is roughly 100+. The exact number isn't important — this is the before-snapshot we'll compare against later.

- [ ] **Step 6: Do NOT commit anything in Task 1**

This task is read-only. Leave the working tree alone. Move on to Task 2.

---

### Task 2: Resolve `pkg/kube/service` collision

Today both packages exist:
- `pkg/kube/service/` (Enterprise) — defines three exported functions: `DeleteServiceIfItExists`, `Merge`, `CreateOrUpdateService`. All three currently take parameters typed against MCO's package: `service.GetDeleter`, `service.GetUpdateCreator`. **The file imports `mongodb-community-operator/pkg/kube/service` as `service`** — an outside→MCO violation in production code today.
- `mongodb-community-operator/pkg/kube/service/` (MCO) — defines the primitives: `Getter`, `Updater`, `Creator`, `Deleter`, `GetDeleter`, `GetUpdateCreator` interfaces (in `service.go`) and the `Apply`/`Modification`/`Builder` builder DSL (in `service_builder.go`).

Direction: **merge MCO's content into the root package**, keep Enterprise's higher-level helpers, delete the MCO copy. Root `pkg/kube/service/` ends up holding everything previously split across both.

**Important: call-site alias rewrite.** Five files in the Enterprise tree import **both** paths simultaneously today:
- `controllers/operator/appdbreplicaset_controller.go`
- `controllers/operator/mongodbshardedcluster_controller.go`
- `controllers/operator/mongodbmultireplicaset_controller.go`
- `controllers/operator/create/create.go`
- `pkg/webhook/setup.go` (only the aliased one)

The convention in these files is:
- `service.X` (bare) refers to MCO's package — used for `service.Builder()`, `service.GetUpdateCreator`, etc.
- `mekoService.X` (aliased) refers to Enterprise's package — used for `mekoService.CreateOrUpdateService`, `mekoService.DeleteServiceIfItExists`.

After the merge both halves live in one package, so the alias becomes redundant. The plan **rewrites every `mekoService.` call site to bare `service.`** and drops the `mekoService` alias before the path-rewrite sweep — otherwise the sweep produces files with two imports of the same path, breaking the build.

**Files:**
- Modify: `pkg/kube/service/service.go` — drop the MCO import, drop `service.` qualifiers on `GetDeleter`/`GetUpdateCreator`, absorb MCO's interface declarations
- Create: `pkg/kube/service/service_builder.go` — moved verbatim from MCO via `git mv`
- Modify: `pkg/kube/service/service_test.go` — only if a test name collides with a moved declaration (Step 2 verifies; expected: no changes)
- Delete: `mongodb-community-operator/pkg/kube/service/service.go` and the empty parent dir
- Modify (5 files): drop `mekoService` alias from import block, rewrite `mekoService.` → `service.` at call sites — `controllers/operator/{appdbreplicaset,mongodbshardedcluster,mongodbmultireplicaset}_controller.go`, `controllers/operator/create/create.go`, `pkg/webhook/setup.go`
- Sweep-update remaining unaliased `"…/mongodb-community-operator/pkg/kube/service"` import paths in all `*.go` files repo-wide.

- [ ] **Step 1: Read both `service.go` files fully**

```bash
wc -l pkg/kube/service/*.go mongodb-community-operator/pkg/kube/service/*.go
```

Then read each file end-to-end. Confirm the following — if any assumption fails, stop and re-plan this task before continuing:

- MCO's `service.go` defines the interfaces `Getter`, `Updater`, `Creator`, `Deleter`, `GetDeleter`, `GetUpdater`, `GetUpdateCreator`, `GetUpdateCreateDeleter` (and possibly more combiner interfaces). No public functions.
- MCO's `service_builder.go` defines the builder DSL: `Apply`, `Modification`, `Builder`, and the various `With...` modifiers for constructing `corev1.Service` objects.
- Enterprise's `service.go` defines three exported functions: `DeleteServiceIfItExists(ctx, getterDeleter service.GetDeleter, serviceName types.NamespacedName) error`, `Merge(dest, source corev1.Service) corev1.Service`, and `CreateOrUpdateService(ctx, getUpdateCreator service.GetUpdateCreator, desiredService corev1.Service) error`. It imports MCO's package as `service` and references `service.GetDeleter` / `service.GetUpdateCreator` by qualified name.
- Enterprise's `service_test.go` exists at ~195 lines — read its package clause (expected `package service` or `package service_test`) and the import block (it should not currently import MCO's package).

- [ ] **Step 2: Identify any naming overlaps between the two files**

```bash
grep -hE '^(func |type |var |const )' \
  pkg/kube/service/*.go \
  mongodb-community-operator/pkg/kube/service/*.go \
  | awk '{print $1, $2}' | sed 's/(.*//' | sort | uniq -d
```

Expected: empty output (no duplicate top-level identifiers). If output is non-empty, the two packages have overlapping symbols and the merge will collide on those names — stop and resolve case by case (rename, or pick the version actually used).

- [ ] **Step 3: Move MCO's `service_builder.go` to root as a new file**

```bash
git mv mongodb-community-operator/pkg/kube/service/service_builder.go pkg/kube/service/service_builder.go
```

Expected: file now lives at `pkg/kube/service/service_builder.go`. Package declaration inside is already `package service` — no edit needed.

- [ ] **Step 4: Merge MCO's `service.go` interfaces into root's `service.go`**

Use the `Edit` tool to modify `pkg/kube/service/service.go`:

1. **Drop the MCO import line** (currently `"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"`).
2. **Insert MCO's interface declarations** verbatim (everything from MCO's `service.go` except the `package service` line and the standard imports) at the top of the file, immediately after the import block. As of the survey, MCO declares: `Getter`, `Updater`, `Creator`, `Deleter`, `GetDeleter`, `GetUpdater`, `GetUpdateCreator`, `GetUpdateCreateDeleter`. Copy whatever the file actually contains.
3. **Drop every `service.` qualifier** in the existing three helper functions: `service.GetDeleter` → `GetDeleter`, `service.GetUpdateCreator` → `GetUpdateCreator` (both at the function-parameter type position).
4. **Adjust the import block** as needed — `"sigs.k8s.io/controller-runtime/pkg/client"` is now required (the interfaces use `client.ObjectKey`); `"k8s.io/apimachinery/pkg/types"` should remain (used by `DeleteServiceIfItExists`/`CreateOrUpdateService`).

Final file shape (illustrative — the actual content is the union of what's in the two source files):

```go
package service

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    apiErrors "k8s.io/apimachinery/pkg/api/errors"
    "k8s.io/apimachinery/pkg/types"
    "sigs.k8s.io/controller-runtime/pkg/client"
)

// Interfaces moved from mongodb-community-operator/pkg/kube/service.
type Getter interface { GetService(ctx context.Context, objectKey client.ObjectKey) (corev1.Service, error) }
type Updater interface { UpdateService(ctx context.Context, service corev1.Service) error }
type Creator interface { CreateService(ctx context.Context, service corev1.Service) error }
type Deleter interface { DeleteService(ctx context.Context, objectKey client.ObjectKey) error }
type GetDeleter interface { Getter; Deleter }
type GetUpdateCreator interface { Getter; Updater; Creator }
// ... plus any additional combiner interfaces from MCO's service.go ...

func DeleteServiceIfItExists(ctx context.Context, getterDeleter GetDeleter, serviceName types.NamespacedName) error { ... }
func Merge(dest corev1.Service, source corev1.Service) corev1.Service { ... }
func CreateOrUpdateService(ctx context.Context, getUpdateCreator GetUpdateCreator, desiredService corev1.Service) error { ... }
```

- [ ] **Step 5: Delete MCO's now-orphaned `service.go` and the empty directory**

```bash
git rm mongodb-community-operator/pkg/kube/service/service.go
rmdir mongodb-community-operator/pkg/kube/service 2>/dev/null || true
ls mongodb-community-operator/pkg/kube/service 2>/dev/null || echo "directory gone"
```

Expected last line: `directory gone`.

- [ ] **Step 6a: Rewrite `mekoService.` call sites to bare `service.` in the 5 dual-import files**

The 5 files were enumerated in this task's "Files" section. For each file, run **two `Edit` tool calls**:

1. `Edit` with `replace_all: true` rewriting `mekoService.` → `service.` at call sites.
2. `Edit` removing the alias line from the import block:
   ```
   	mekoService "github.com/mongodb/mongodb-kubernetes/pkg/kube/service"
   ```

Special case — **`pkg/webhook/setup.go`** has only the aliased form (no plain MCO-path import to pair with it). For this one file, the second `Edit` *replaces* the alias line with the plain import line `"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"` rather than removing it.

Verify all `mekoService` identifiers and import lines are gone:

```bash
git grep -nE 'mekoService' -- '*.go' || echo "no mekoService refs"
```

Expected: `no mekoService refs`.

- [ ] **Step 6b: Sweep the remaining unaliased MCO service import path**

Use `perl -i -pe` — works identically on macOS and Linux, no BSD-vs-GNU quirk:

```bash
git grep -l '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"' -- '*.go' \
  | xargs perl -i -pe 's|"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"|"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"|g'
```

(Note: `gomvpkg` is **not** an option here — it refuses to run when a package already exists at the destination path, which is the entire premise of this task.)

Verify no stragglers:

```bash
git grep -n '"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/service"' -- '*.go' || echo "no stragglers"
```

Expected: `no stragglers`.

- [ ] **Step 6c: Verify no file has duplicate `pkg/kube/service` imports**

```bash
git grep -c '"github.com/mongodb/mongodb-kubernetes/pkg/kube/service"' -- '*.go' \
  | awk -F: '$2 > 1 { print }' | tee /tmp/decuple-dup-check.txt
```

Expected: empty output. If any file appears, it has multiple import lines for the same path (a build error) — open the file and remove the duplicate.

- [ ] **Step 7: Normalise import blocks**

```bash
gofmt -l pkg/kube/service/ mongodb-community-operator/ controllers/ pkg/ cmd/
```

Expected: no files listed. If any file is listed, run `gofmt -w` on it.

```bash
goimports -w pkg/kube/service/ controllers/operator/ controllers/operator/create/ pkg/webhook/
```

(Limit `goimports -w` to the directories actually modified — running it on `./...` is slow and produces noisy diffs on unrelated files.)

- [ ] **Step 8: Build**

```bash
go build ./...
```

Expected: exit 0, no errors.

If it fails on a missing symbol, the most likely cause is a missed `service.` qualifier in `pkg/kube/service/service.go`. Re-read the file and remove every `service.<symbol>` that refers to a type now declared in the same package.

If it fails on `duplicate import` or `redeclared`, return to Step 6c — a file still has the same path imported twice.

- [ ] **Step 9: Run service-package tests**

```bash
go test -count=1 ./pkg/kube/service/...
```

Expected: PASS.

- [ ] **Step 10: Run all tests in `./controllers/operator/...` and `./pkg/webhook/...`** (the largest known consumers of `pkg/kube/service`)

```bash
go test -short -count=1 -timeout 10m ./controllers/operator/... ./pkg/webhook/...
```

Expected: PASS, no FAIL. Runtime: ~3–6 minutes locally on Apple Silicon; longer in CI.

- [ ] **Step 11: Run full short-mode test suite and diff against baseline**

```bash
go test -short -count=1 -timeout 15m ./... 2>&1 | tee /tmp/decuple-task2.txt | tail -20
diff <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-baseline.txt | sort) \
     <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-task2.txt | sort)
```

Expected: no diff lines (the same packages pass/fail identically as the baseline). Runtime: ~8–15 min total.

If `go test -short ./...` exceeded the timeout, increase `-timeout`; if any test is flaky, re-run only the flaky package and confirm it's not a regression introduced by this change.

- [ ] **Step 12: Commit**

```bash
git add pkg/kube/service/
git add -u  # picks up the deleted MCO file and all consumer edits
git status --short
```

Verify the staged list contains:
- `M pkg/kube/service/service.go`
- `A pkg/kube/service/service_builder.go` (or `R` rename if Git detects the move)
- `D mongodb-community-operator/pkg/kube/service/service.go`
- 5 `M` entries for the consumer files
- Plus whatever extra files the sweep touched

Commit. The repo mixes Jira-prefixed (`KUBE-XX:`, `CLOUDP-XX:`) and Conventional-Commits (`refactor:`, `fix:`) styles. If a Jira ticket has been opened for this work, prefix with that; otherwise the `refactor:` style below is acceptable:

```bash
git commit -m "$(cat <<'EOF'
refactor: merge mongodb-community-operator/pkg/kube/service into pkg/kube/service

Resolve the name collision between Enterprise pkg/kube/service (helpers:
DeleteServiceIfItExists, Merge, CreateOrUpdateService) and MCO's
mongodb-community-operator/pkg/kube/service (primitives: Getter, Updater,
Creator, Deleter, GetDeleter, GetUpdateCreator interfaces, plus the Builder
DSL in service_builder.go).

Enterprise's package previously imported MCO's package as a dependency
(an outside->MCO violation in production code). After this commit, both
sets of declarations live in a single pkg/kube/service package at repo root,
the MCO copy is deleted, and the mekoService alias used in five Enterprise
files is replaced by bare service.* references.

Foundation step for the mongodb-community-operator decoupling
(spec: docs/design/decouple-mdb-community-investigation.md, cluster 1).
EOF
)"
```

Confirm:

```bash
git status --short
git log -1 --stat | head -40
```

Expected: clean working tree apart from the still-uncommitted `docs/design/` files. The commit stat shows additions/modifications in `pkg/kube/service/`, deletions in `mongodb-community-operator/pkg/kube/service/`, and modifications across the 5 consumer files plus any other files the path sweep touched.

---

End of Chunk 1.

---

## Chunk 2: move the 5 util leaf packages to root

This chunk relocates `pkg/util/{constants, contains, envvar, apierrors, scale}` from MCO to root in a **single batched commit**. They are batched because:

- All five have **zero intra-MCO dependencies on packages outside this batch** (verified during planning).
- `util/contains` imports `util/constants`; both are in this batch, so the move is internally consistent and leaves the build green at the commit boundary.
- Each package is small (1–2 files, < 100 LoC) — no benefit to splitting commits.

The kube packages (Chunk 3) and the larger util/merge (later chunk) are deliberately deferred.

### Task 3: Move 5 util leaf packages to root

**Files (moved):**
- `mongodb-community-operator/pkg/util/constants/` → `pkg/util/constants/`
- `mongodb-community-operator/pkg/util/contains/` → `pkg/util/contains/`
- `mongodb-community-operator/pkg/util/envvar/` → `pkg/util/envvar/`
- `mongodb-community-operator/pkg/util/apierrors/` → `pkg/util/apierrors/`
- `mongodb-community-operator/pkg/util/scale/` → `pkg/util/scale/`

**Files (modified):**
- `pkg/util/contains/contains.go` (intra-package import rewrite: `util/constants` from MCO path → root path)
- Every `*.go` file repo-wide that currently imports any of the 5 moved paths.

**Pre-flight assumption** (verified at planning time): none of `pkg/util/{constants, contains, envvar, apierrors, scale}` exists at repo root today. There is a flat file `pkg/util/constants.go` declared in `package util` — that is a **different package** from `pkg/util/constants/` (subdir, package `constants`) and does not collide. Step 1 re-verifies before any move.

- [ ] **Step 1: Re-verify no collisions at root**

```bash
for p in constants contains envvar apierrors scale; do
  if [ -d "pkg/util/$p" ]; then echo "COLLISION: pkg/util/$p exists"; else echo "ok: pkg/util/$p free"; fi
done
```

Expected: 5 lines of `ok: pkg/util/$p free`. If any `COLLISION` appears, stop and surface to the user — the plan assumes free destinations and a collision means a prior plan step is needed.

- [ ] **Step 2: Confirm working tree is clean apart from Chunk 1's commit and the still-uncommitted spec/plan**

```bash
git status --short
```

Expected: at most `?? docs/design/` plus `?? .idea/...` files. If you see staged or unstaged `M`/`A`/`D` changes to Go files, stop and resolve.

- [ ] **Step 3: Move all 5 directories with `git mv`**

```bash
git mv mongodb-community-operator/pkg/util/constants pkg/util/constants
git mv mongodb-community-operator/pkg/util/contains  pkg/util/contains
git mv mongodb-community-operator/pkg/util/envvar    pkg/util/envvar
git mv mongodb-community-operator/pkg/util/apierrors pkg/util/apierrors
git mv mongodb-community-operator/pkg/util/scale     pkg/util/scale
```

Verify the destinations exist and the MCO directories are gone:

```bash
ls -d pkg/util/{constants,contains,envvar,apierrors,scale}
ls mongodb-community-operator/pkg/util/{constants,contains,envvar,apierrors,scale} 2>/dev/null \
  || echo "MCO copies gone"
```

Expected: 5 destination dirs present; final line `MCO copies gone`.

- [ ] **Step 4: Rewrite the intra-package import in `pkg/util/contains/contains.go`**

Open the file. It currently has:

```go
import (
    "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/constants"
)
```

Use `Edit` to replace with:

```go
import (
    "github.com/mongodb/mongodb-kubernetes/pkg/util/constants"
)
```

(If the import block also has stdlib imports, preserve them — only the one path changes.)

- [ ] **Step 5: Sweep all 5 import paths repo-wide**

```bash
for p in constants contains envvar apierrors scale; do
  echo "--- sweeping util/$p ---"
  git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/$p\"" -- '*.go' \
    | xargs -r perl -i -pe "s|\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/$p\"|\"github.com/mongodb/mongodb-kubernetes/pkg/util/$p\"|g"
done
```

(`xargs -r` skips the rewrite if the grep returned nothing — important for packages with zero outside importers. On macOS BSD `xargs`, `-r` is a no-op because the BSD default *already* skips empty-stdin invocations; on Linux GNU `xargs`, `-r` is required. Same flag, works on both.)

Verify no stragglers:

```bash
for p in constants contains envvar apierrors scale; do
  hits=$(git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/$p\"" -- '*.go' | wc -l | tr -d ' ')
  echo "util/$p stragglers: $hits"
done
```

Expected: 5 lines all ending `: 0`.

- [ ] **Step 6: Verify no file ends up with duplicate imports for any of the 5 paths**

```bash
for p in constants contains envvar apierrors scale; do
  git grep -c "\"github.com/mongodb/mongodb-kubernetes/pkg/util/$p\"" -- '*.go' \
    | awk -F: '$2 > 1 { print }'
done
```

Expected: empty output. (Same caveat as Chunk 1 Step 6c: real duplicate-import bugs also surface at the `go build` step below.)

- [ ] **Step 7: Normalise import blocks**

```bash
goimports -w pkg/util/constants/ pkg/util/contains/ pkg/util/envvar/ pkg/util/apierrors/ pkg/util/scale/
```

If there are stragglers under `controllers/`, `mongodb-community-operator/`, or `pkg/`, run `goimports -w` on those directories too — but in practice the path sweep already produces gofmt-clean output for unaliased imports.

- [ ] **Step 8: Build**

```bash
go build ./...
```

Expected: exit 0, no errors.

If a file fails with `imported and not used: "...mongodb-community-operator/pkg/util/<p>"`, that's a stale import line; rerun Step 5 for the affected path and re-run `goimports`.

- [ ] **Step 9: Run unit tests for the 5 moved packages plus their heaviest consumers**

```bash
go test -count=1 -timeout 5m \
  ./pkg/util/constants/... \
  ./pkg/util/contains/... \
  ./pkg/util/envvar/... \
  ./pkg/util/apierrors/... \
  ./pkg/util/scale/... \
  ./controllers/operator/... \
  ./controllers/om/... \
  ./pkg/telemetry/...
```

Expected: PASS. Runtime: ~3–5 min. (`pkg/telemetry/` is included because it is the heaviest non-MCO consumer of `pkg/util/envvar` — 4 files — and a regression there would otherwise wait for Step 10's full run.)

- [ ] **Step 10: Full short-mode test suite + diff against baseline**

```bash
go test -short -count=1 -timeout 15m ./... 2>&1 | tee /tmp/decuple-task3.txt | tail -20
diff <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-baseline.txt | sort) \
     <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-task3.txt | sort)
```

Expected: no diff lines.

- [ ] **Step 11: Commit**

```bash
git add pkg/util/
git add -u  # stages the deletions in mongodb-community-operator/pkg/util/ and the consumer edits
git status --short | head -30
```

Verify the staged list:
- 5 renames (`R`) from `mongodb-community-operator/pkg/util/<p>/...` to `pkg/util/<p>/...`
- 1 modification (`M pkg/util/contains/contains.go`) for the intra-package import rewrite
- N `M` entries for repo-wide importers (typically dozens — Enterprise's util/merge importer count alone is ~20)

Commit (Jira prefix if a ticket exists; otherwise the `refactor:` form):

```bash
git commit -m "$(cat <<'EOF'
refactor: move 5 util leaf packages from MCO to root pkg/util

Relocate the following packages from mongodb-community-operator/pkg/util/
to pkg/util/ at repo root:
- constants
- contains  (intra-package import to constants also updated)
- envvar
- apierrors
- scale

All 5 are pure leaves (no transitive MCO dependencies among themselves
outside this batch). Behaviour-preserving refactor — code unchanged,
only import paths rewritten.

Foundation step for the mongodb-community-operator decoupling
(spec: docs/design/decouple-mdb-community-investigation.md, cluster 5).
EOF
)"
```

Confirm:

```bash
git log -1 --stat | head -50
```

Expected: stat shows 5 directory renames, 1 modification inside `pkg/util/contains/`, and modifications across the consumers.

---

End of Chunk 2.

---

## Chunk 3: move the 7 kube leaf packages to root

Relocates `pkg/kube/{annotations, configmap, lifecycle, pod, probes, resourcerequirements, persistentvolumeclaim}` from MCO to root in a **single batched commit**. All seven are pure leaves with **zero MCO imports** of any kind (verified at planning time), so the batch is internally consistent.

Note: `pkg/kube/service` was relocated in Chunk 1 (collision case). `pkg/kube/secret`, `pkg/kube/container`, `pkg/kube/client`, `pkg/kube/podtemplatespec` have intra-MCO dependencies and are handled in later chunks once their dependencies have moved.

### Task 4: Move 7 kube leaf packages to root

**Files (moved):**
- `mongodb-community-operator/pkg/kube/annotations/` → `pkg/kube/annotations/`
- `mongodb-community-operator/pkg/kube/configmap/` → `pkg/kube/configmap/`
- `mongodb-community-operator/pkg/kube/lifecycle/` → `pkg/kube/lifecycle/`
- `mongodb-community-operator/pkg/kube/pod/` → `pkg/kube/pod/`
- `mongodb-community-operator/pkg/kube/probes/` → `pkg/kube/probes/`
- `mongodb-community-operator/pkg/kube/resourcerequirements/` → `pkg/kube/resourcerequirements/`
- `mongodb-community-operator/pkg/kube/persistentvolumeclaim/` → `pkg/kube/persistentvolumeclaim/`

**Files (modified):**
- Every `*.go` file repo-wide that imports any of the 7 moved paths. No intra-package import rewrites required (these are all leaves).

- [ ] **Step 1: Re-verify no collisions at root**

```bash
for p in annotations configmap lifecycle pod probes resourcerequirements persistentvolumeclaim; do
  if [ -d "pkg/kube/$p" ]; then echo "COLLISION: pkg/kube/$p exists"; else echo "ok: pkg/kube/$p free"; fi
done
```

Expected: 7 lines of `ok: pkg/kube/$p free`. If any `COLLISION` appears, stop and surface to the user.

- [ ] **Step 2: Re-verify the seven packages have no MCO imports**

```bash
for p in annotations configmap lifecycle pod probes resourcerequirements persistentvolumeclaim; do
  hits=$(grep -l "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/kube/$p/*.go 2>/dev/null | wc -l | tr -d ' ')
  echo "kube/$p MCO imports: $hits"
done
```

Expected: 7 lines all ending `: 0`. If any package has non-zero MCO imports, the batched-commit premise breaks — stop and either add the dependency to this chunk or move the package to a later chunk.

- [ ] **Step 3: Confirm working tree is clean apart from prior Plan-1 commits and the still-uncommitted spec/plan**

```bash
git status --short
```

Expected: at most `?? docs/design/` plus `?? .idea/...` files. Nothing else staged or unstaged.

- [ ] **Step 4: Move all 7 directories with `git mv`**

```bash
git mv mongodb-community-operator/pkg/kube/annotations             pkg/kube/annotations
git mv mongodb-community-operator/pkg/kube/configmap               pkg/kube/configmap
git mv mongodb-community-operator/pkg/kube/lifecycle               pkg/kube/lifecycle
git mv mongodb-community-operator/pkg/kube/pod                     pkg/kube/pod
git mv mongodb-community-operator/pkg/kube/probes                  pkg/kube/probes
git mv mongodb-community-operator/pkg/kube/resourcerequirements    pkg/kube/resourcerequirements
git mv mongodb-community-operator/pkg/kube/persistentvolumeclaim   pkg/kube/persistentvolumeclaim
```

Verify:

```bash
ls -d pkg/kube/{annotations,configmap,lifecycle,pod,probes,resourcerequirements,persistentvolumeclaim}
ls mongodb-community-operator/pkg/kube/{annotations,configmap,lifecycle,pod,probes,resourcerequirements,persistentvolumeclaim} 2>/dev/null \
  || echo "MCO copies gone"
```

Expected: 7 destination dirs present; final line `MCO copies gone`.

- [ ] **Step 5: Sweep all 7 import paths repo-wide**

```bash
for p in annotations configmap lifecycle pod probes resourcerequirements persistentvolumeclaim; do
  echo "--- sweeping kube/$p ---"
  git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/$p\"" -- '*.go' \
    | xargs -r perl -i -pe "s|\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/$p\"|\"github.com/mongodb/mongodb-kubernetes/pkg/kube/$p\"|g"
done
```

Verify no stragglers:

```bash
for p in annotations configmap lifecycle pod probes resourcerequirements persistentvolumeclaim; do
  hits=$(git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/$p\"" -- '*.go' | wc -l | tr -d ' ')
  echo "kube/$p stragglers: $hits"
done
```

Expected: 7 lines all ending `: 0`.

- [ ] **Step 6: Verify no file ends up with duplicate imports for any of the 7 paths**

```bash
for p in annotations configmap lifecycle pod probes resourcerequirements persistentvolumeclaim; do
  git grep -c "\"github.com/mongodb/mongodb-kubernetes/pkg/kube/$p\"" -- '*.go' \
    | awk -F: '$2 > 1 { print }'
done
```

Expected: empty output. (As in Chunk 1/2, the `go build` step below catches any real duplicate-import bug regardless.)

- [ ] **Step 7: Normalise import blocks**

```bash
goimports -w pkg/kube/annotations/ pkg/kube/configmap/ pkg/kube/lifecycle/ pkg/kube/pod/ \
              pkg/kube/probes/ pkg/kube/resourcerequirements/ pkg/kube/persistentvolumeclaim/
```

- [ ] **Step 8: Build**

```bash
go build ./...
```

Expected: exit 0, no errors.

If a file fails with `imported and not used: "...mongodb-community-operator/pkg/kube/<p>"`, rerun Step 5 for the affected path and re-run `goimports`.

- [ ] **Step 9: Run unit tests for the 7 moved packages plus their heaviest consumers**

```bash
go test -count=1 -timeout 5m \
  ./pkg/kube/annotations/... \
  ./pkg/kube/configmap/... \
  ./pkg/kube/lifecycle/... \
  ./pkg/kube/pod/... \
  ./pkg/kube/probes/... \
  ./pkg/kube/resourcerequirements/... \
  ./pkg/kube/persistentvolumeclaim/... \
  ./controllers/operator/... \
  ./mongodb-community-operator/...
```

Expected: PASS. Runtime: ~3–5 min. (`controllers/operator/` is the dominant consumer for the kube cluster — 47 importers of `pkg/kube/client` alone, with proportional shares across `configmap`, `annotations`, etc. `./mongodb-community-operator/...` is also included because `pkg/kube/pod` and `pkg/kube/resourcerequirements` have only MCO-side consumers; ~30s of additional runtime is the cheap insurance.)

- [ ] **Step 10: Full short-mode test suite + diff against baseline**

```bash
go test -short -count=1 -timeout 15m ./... 2>&1 | tee /tmp/decuple-task4.txt | tail -20
diff <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-baseline.txt | sort) \
     <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-task4.txt | sort)
```

Expected: no diff lines.

- [ ] **Step 11: Commit**

```bash
git add pkg/kube/
git add -u  # stages MCO deletions and all consumer edits
git status --short | head -30
```

Verify staged content:
- 7 renames (`R`) from `mongodb-community-operator/pkg/kube/<p>/...` to `pkg/kube/<p>/...`
- N `M` entries for repo-wide importers (dozens; `controllers/operator/` will dominate)
- No modifications inside the moved packages themselves (these are leaves, nothing to rewrite internally)

```bash
git commit -m "$(cat <<'EOF'
refactor: move 7 kube leaf packages from MCO to root pkg/kube

Relocate the following packages from mongodb-community-operator/pkg/kube/
to pkg/kube/ at repo root:
- annotations
- configmap
- lifecycle
- pod
- probes
- resourcerequirements
- persistentvolumeclaim

All 7 are pure leaves (zero MCO imports). Behaviour-preserving refactor —
code unchanged, only import paths rewritten.

Foundation step for the mongodb-community-operator decoupling
(spec: docs/design/decouple-mdb-community-investigation.md, cluster 1).
EOF
)"
```

Confirm:

```bash
git log -1 --stat | head -50
```

Expected: stat shows 7 directory renames and modifications across the consumers.

---

End of Chunk 3.

---

## Chunk 4: move the 5 non-leaf packages to root

Relocates the remaining 5 in Plan 1's scope:

- `pkg/kube/secret` (depends on `util/contains` — at root post-Chunk-2)
- `pkg/kube/container` (depends on `kube/{lifecycle,probes,resourcerequirements}` at root post-Chunk-3 and `util/envvar` at root post-Chunk-2)
- `pkg/kube/client` (depends on `kube/{configmap,pod,service}` at root post-Chunks-1/3 and `kube/secret` moving this chunk)
- `pkg/kube/podtemplatespec` (depends on `kube/container` and `util/merge` moving this chunk, and `util/envvar` at root post-Chunk-2)
- `pkg/util/merge` (depends on `kube/{container,probes}` — container moving this chunk, probes at root post-Chunk-3 — and `util/contains` at root post-Chunk-2)

All five move in a **single batched commit**. The intra-moved-package imports (e.g. `kube/client` importing `kube/secret`) get rewritten correctly by the repo-wide sweep because it rewrites every file in the repo — including files inside the packages being moved this chunk.

### Residual transient outside→MCO edge (acknowledged, fixed by Plan 2)

`pkg/util/merge` imports **`mongodb-community-operator/pkg/automationconfig`** (via the merge helper that operates on `automationconfig` structs). `pkg/automationconfig` is out of Plan 1's scope — it moves in Plan 2.

After Plan 1 lands, the working tree will therefore contain exactly one transient cross-boundary import:

```
github.com/mongodb/mongodb-kubernetes/pkg/util/merge
  → github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig
```

This is acceptable because:
- `depguard` is not yet enforcing the boundary rule (that's Plan 10).
- Plan 2 (move `pkg/automationconfig` to root) sweeps this import path as part of its own work.

The Plan 1 final-state verification (Chunk 5) must allow this single residual; the Plan 2 final-state verification removes it.

### Task 5: Move 5 non-leaf packages to root

**Files (moved):**
- `mongodb-community-operator/pkg/kube/secret/` → `pkg/kube/secret/`
- `mongodb-community-operator/pkg/kube/container/` → `pkg/kube/container/`
- `mongodb-community-operator/pkg/kube/client/` → `pkg/kube/client/`
- `mongodb-community-operator/pkg/kube/podtemplatespec/` → `pkg/kube/podtemplatespec/`
- `mongodb-community-operator/pkg/util/merge/` → `pkg/util/merge/`

**Files (modified):** every `*.go` file repo-wide that imports any of the 5 moved paths. The intra-moved-package imports are handled implicitly by the sweep.

- [ ] **Step 1: Re-verify destinations are free at root**

```bash
for p in secret container client podtemplatespec; do
  if [ -d "pkg/kube/$p" ]; then echo "COLLISION: pkg/kube/$p exists"; else echo "ok: pkg/kube/$p free"; fi
done
if [ -d "pkg/util/merge" ]; then echo "COLLISION: pkg/util/merge exists"; else echo "ok: pkg/util/merge free"; fi
```

Expected: 5 lines of `ok`. If any `COLLISION` appears, stop.

- [ ] **Step 2: Re-verify the current intra-MCO imports inside these 5 packages match the post-Chunks-2+3 expectations**

```bash
echo "--- mongodb-community-operator/pkg/kube/secret ---"
grep -h "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/kube/secret/*.go 2>/dev/null | sort -u
echo "--- mongodb-community-operator/pkg/kube/container ---"
grep -h "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/kube/container/*.go 2>/dev/null | sort -u
echo "--- mongodb-community-operator/pkg/kube/client ---"
grep -h "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/kube/client/*.go 2>/dev/null | sort -u
echo "--- mongodb-community-operator/pkg/kube/podtemplatespec ---"
grep -h "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/kube/podtemplatespec/*.go 2>/dev/null | sort -u
echo "--- mongodb-community-operator/pkg/util/merge ---"
grep -h "mongodb-community-operator/pkg/" mongodb-community-operator/pkg/util/merge/*.go 2>/dev/null | sort -u
```

Expected post-Chunks-2+3:
- `kube/secret`: no MCO imports (was importing `util/contains`, rewritten by Chunk 2's sweep).
- `kube/container`: no MCO imports.
- `kube/client`: only `mongodb-community-operator/pkg/kube/secret` (kube/secret is being moved this chunk).
- `kube/podtemplatespec`: only `mongodb-community-operator/pkg/kube/container` and `mongodb-community-operator/pkg/util/merge` (both being moved this chunk).
- `util/merge`: only `mongodb-community-operator/pkg/kube/container` (moving this chunk) and `mongodb-community-operator/pkg/automationconfig` (the documented residual — stays).

If any unexpected MCO imports appear, stop and surface — earlier chunks may have skipped a sweep.

- [ ] **Step 3: Confirm working tree is clean apart from prior Plan-1 commits and the still-uncommitted spec/plan**

```bash
git status --short
```

Expected: at most `?? docs/design/` plus IDE files.

- [ ] **Step 4: Move all 5 directories with `git mv`**

```bash
git mv mongodb-community-operator/pkg/kube/secret           pkg/kube/secret
git mv mongodb-community-operator/pkg/kube/container        pkg/kube/container
git mv mongodb-community-operator/pkg/kube/client           pkg/kube/client
git mv mongodb-community-operator/pkg/kube/podtemplatespec  pkg/kube/podtemplatespec
git mv mongodb-community-operator/pkg/util/merge            pkg/util/merge
```

Verify:

```bash
ls -d pkg/kube/{secret,container,client,podtemplatespec} pkg/util/merge
ls mongodb-community-operator/pkg/kube/{secret,container,client,podtemplatespec} mongodb-community-operator/pkg/util/merge 2>/dev/null \
  || echo "MCO copies gone"
```

Expected: 5 destination dirs present; final line `MCO copies gone`.

- [ ] **Step 5: Sweep all 5 import paths repo-wide**

```bash
for p in kube/secret kube/container kube/client kube/podtemplatespec util/merge; do
  echo "--- sweeping $p ---"
  git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/$p\"" -- '*.go' \
    | xargs -r perl -i -pe "s|\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/$p\"|\"github.com/mongodb/mongodb-kubernetes/pkg/$p\"|g"
done
```

The sweep also rewrites the intra-moved-package imports (`kube/client → kube/secret`, `kube/podtemplatespec → kube/container/util/merge`, `util/merge → kube/container`) because `perl -i -pe` operates on every file in the repo, including those inside the just-moved directories.

Verify no stragglers for the 5 paths:

```bash
for p in kube/secret kube/container kube/client kube/podtemplatespec util/merge; do
  hits=$(git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/$p\"" -- '*.go' | wc -l | tr -d ' ')
  echo "$p stragglers: $hits"
done
```

Expected: 5 lines all ending `: 0`.

- [ ] **Step 6: Verify the documented residual exists and nothing else slipped through**

```bash
git grep -l 'mongodb-community-operator/pkg/automationconfig' -- pkg/util/merge/
git grep -l 'mongodb-community-operator/pkg/automationconfig' -- pkg/util/merge/ | wc -l | tr -d ' '
```

Expected: exactly **2** matches — `pkg/util/merge/merge_automationconfigs.go` and `pkg/util/merge/merge_automationconfigs_test.go`. If the count is `0` the sweep silently removed the import (a bug — `automationconfig` must remain reachable from util/merge). If the count is anything other than 2, re-inspect before continuing.

Then check there are no other outside→MCO imports from the relocated packages:

```bash
git grep -nE '"github\.com/mongodb/mongodb-kubernetes/mongodb-community-operator/' -- pkg/util/merge/ pkg/kube/secret/ pkg/kube/container/ pkg/kube/client/ pkg/kube/podtemplatespec/ \
  | grep -v 'pkg/automationconfig'
```

Expected: empty output. Anything else is a sweep miss and must be fixed before continuing.

- [ ] **Step 7: Verify no file ends up with duplicate imports for any of the 5 paths**

```bash
for p in kube/secret kube/container kube/client kube/podtemplatespec util/merge; do
  git grep -c "\"github.com/mongodb/mongodb-kubernetes/pkg/$p\"" -- '*.go' \
    | awk -F: '$2 > 1 { print }'
done
```

Expected: empty output.

- [ ] **Step 8: Normalise import blocks**

```bash
goimports -w pkg/kube/secret/ pkg/kube/container/ pkg/kube/client/ pkg/kube/podtemplatespec/ pkg/util/merge/
```

- [ ] **Step 9: Build**

```bash
go build ./...
```

Expected: exit 0, no errors.

If a file fails with `imported and not used: "...mongodb-community-operator/pkg/<p>"`, rerun Step 5 for the affected path and re-run `goimports`.

- [ ] **Step 10: Run unit tests for the 5 moved packages plus their heaviest consumers**

```bash
go test -count=1 -timeout 10m \
  ./pkg/kube/secret/... \
  ./pkg/kube/container/... \
  ./pkg/kube/client/... \
  ./pkg/kube/podtemplatespec/... \
  ./pkg/util/merge/... \
  ./controllers/operator/... \
  ./mongodb-community-operator/...
```

Expected: PASS. Runtime: ~5–8 min. (`controllers/operator/` and `mongodb-community-operator/` together cover essentially every consumer of these 5 packages.)

- [ ] **Step 11: Full short-mode test suite + diff against baseline**

```bash
go test -short -count=1 -timeout 15m ./... 2>&1 | tee /tmp/decuple-task5.txt | tail -20
diff <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-baseline.txt | sort) \
     <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-task5.txt | sort)
```

Expected: no diff lines.

- [ ] **Step 12: Commit**

```bash
git add pkg/kube/ pkg/util/
git add -u  # stages MCO deletions and consumer edits
git status --short | head -40
```

Verify staged content:
- 5 renames (`R`) from `mongodb-community-operator/pkg/kube|util/<p>/...` to `pkg/kube|util/<p>/...`
- N `M` entries for repo-wide importers (this batch touches the heaviest consumers: `pkg/kube/client` has 47 importers, `pkg/util/merge` 20, `pkg/kube/secret` 18, `pkg/kube/container` 12, `pkg/kube/podtemplatespec` 8 — expect dozens of `M` entries)

```bash
git commit -m "$(cat <<'EOF'
refactor: move 5 non-leaf shared packages from MCO to root

Relocate the following packages from mongodb-community-operator/pkg/ to
pkg/ at repo root:
- kube/secret
- kube/container
- kube/client
- kube/podtemplatespec
- util/merge

These five have intra-batch dependencies (e.g. kube/client uses kube/secret;
kube/podtemplatespec uses kube/container and util/merge). The batched move
keeps the build green at every commit boundary.

One transient outside->MCO import remains after this commit and is
deliberately deferred to Plan 2 (move pkg/automationconfig):
  pkg/util/merge -> mongodb-community-operator/pkg/automationconfig

Foundation step for the mongodb-community-operator decoupling
(spec: docs/design/decouple-mdb-community-investigation.md, cluster 1+5).
EOF
)"
```

Confirm:

```bash
git log -1 --stat | head -60
```

Expected: stat shows 5 directory renames plus modifications across dozens of consumers.

---

End of Chunk 4.

---

## Chunk 5: final verification

No new commits in this chunk unless an issue is found. The purpose is to confirm Plan 1 is complete: 18 packages moved, the single documented residual is the only outside→MCO edge in the Plan-1 scope, and behaviour is unchanged.

### Task 6: Verify Plan 1 final state

**Files (read-only):** none modified unless an issue surfaces.

- [ ] **Step 1: Verify `mongodb-community-operator/pkg/kube/` is empty**

All 12 MCO kube subpackages should be gone (1 in Chunk 1, 7 in Chunk 3, 4 in Chunk 4).

```bash
ls mongodb-community-operator/pkg/kube/ 2>/dev/null
```

Expected: empty output (or "No such file or directory"). If anything remains, name it — the most likely cause is a missed `git mv` in an earlier chunk; investigate and either move it or surface to the user.

- [ ] **Step 2: Verify `mongodb-community-operator/pkg/util/` retains exactly the expected residual subpackages**

After Plan 1, MCO `pkg/util/` should contain exactly: `functions, generate, result, state, status, versions` (the 6 subpackages not in Plan 1's scope). The 6 that moved: `apierrors, constants, contains, envvar, merge, scale`.

```bash
ls mongodb-community-operator/pkg/util/ | sort
```

Expected output (one per line):

```
functions
generate
result
state
status
versions
```

If the listing differs (extra subdir from a missed move, or missing subdir from an over-zealous move), surface immediately.

- [ ] **Step 3: Verify Plan-1-scope outside→MCO imports are all gone except the documented residual**

The hard rule for Plan 1: outside `mongodb-community-operator/` may not import any package under it **for any path moved by Plan 1**. The one allowed survivor is `pkg/util/merge → mongodb-community-operator/pkg/automationconfig` (handled by Plan 2).

```bash
# All outside→MCO imports for Plan-1-moved paths (should be zero):
echo "--- moved kube packages ---"
for p in annotations client configmap container lifecycle persistentvolumeclaim pod podtemplatespec probes resourcerequirements secret service; do
  hits=$(git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/$p\"" -- '*.go' | wc -l | tr -d ' ')
  [ "$hits" -gt 0 ] && echo "STRAGGLER kube/$p: $hits files" || echo "ok kube/$p: 0"
done
echo "--- moved util packages ---"
for p in apierrors constants contains envvar merge scale; do
  hits=$(git grep -l "\"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/$p\"" -- '*.go' | wc -l | tr -d ' ')
  [ "$hits" -gt 0 ] && echo "STRAGGLER util/$p: $hits files" || echo "ok util/$p: 0"
done
```

Expected: 18 lines all starting with `ok`. Any `STRAGGLER` line means a Chunk-2/3/4 sweep missed a file; open the file, find why the sweep didn't catch it (alias? unusual quoting? generated file in a directory the sweep didn't traverse?), and fix in a follow-up commit.

- [ ] **Step 4: Verify the single documented residual**

```bash
git grep -l 'mongodb-community-operator/pkg/automationconfig' -- pkg/util/merge/ | wc -l | tr -d ' '
```

Expected: exactly `2` (the two `merge_automationconfigs*.go` files).

Then confirm no other root-level package has any outside→MCO import:

```bash
git grep -nE '"github\.com/mongodb/mongodb-kubernetes/mongodb-community-operator/' -- \
  ':(exclude)mongodb-community-operator/' ':(exclude)docs/' '*.go' \
  | grep -v 'pkg/util/merge/merge_automationconfigs' \
  | head -50
```

Expected: many lines — these are the remaining outside→MCO imports for packages NOT in Plan 1's scope (e.g. `pkg/automationconfig`, `pkg/agent`, `api/v1`, etc., which are addressed by Plans 2–9). They are all expected. Plan 1 only owns the kube/util cluster.

If you want a focused view of what remains, group by destination package:

```bash
git grep -h 'mongodb-community-operator/' -- '*.go' \
  | grep -v '^mongodb-community-operator/' \
  | grep -oE 'mongodb-community-operator/[^"]+' \
  | sort -u
```

Expected: a list of paths like `mongodb-community-operator/api/v1`, `mongodb-community-operator/pkg/agent`, `mongodb-community-operator/pkg/automationconfig`, etc. — none of which include `pkg/kube/...` or any of the 6 moved `pkg/util/{apierrors,constants,contains,envvar,merge,scale}` paths.

- [ ] **Step 5: Full short-mode test suite final diff against the Plan-1 baseline**

```bash
go test -short -count=1 -timeout 15m ./... 2>&1 | tee /tmp/decuple-final.txt | tail -20
diff <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-baseline.txt | sort) \
     <(grep -E '^(ok|FAIL|---) ' /tmp/decuple-final.txt | sort)
```

Expected: no diff. If diff is non-empty, behaviour drifted — investigate.

- [ ] **Step 6: Run `golangci-lint`**

The repo's `.golangci.yml` ships in the toolchain (active linters: `dupl, errcheck, forbidigo, goconst, gosec, govet, ineffassign, rowserrcheck, staticcheck, unconvert, unused`). There is no `make lint` target — invoke `golangci-lint` directly.

Primary command (full repo):

```bash
golangci-lint run ./... 2>&1 | tail -30
```

Expected: no new lint errors compared to the pre-Plan-1 state. If lint fails on a new issue (e.g. an `unused` import survived a sweep, or a `goimports` ordering tweak in a moved file), fix in a follow-up commit before Plan 1 is declared done.

If the full repo run is too slow, a tight-scope check covers the heavy areas:

```bash
golangci-lint run ./pkg/kube/... ./pkg/util/... ./controllers/operator/... ./mongodb-community-operator/...
```

- [ ] **Step 7: Summarise Plan 1 completion**

Run a final summary diff and capture commit hashes:

```bash
echo "=== Plan 1 commits ==="
git log --oneline origin/master..HEAD

echo
echo "=== Plan 1 file changes ==="
git diff --stat origin/master..HEAD | tail -20

echo
echo "=== Remaining MCO subpackages by area ==="
{
  echo "mongodb-community-operator/pkg/:"
  ls -d mongodb-community-operator/pkg/*/ 2>/dev/null
  echo
  echo "mongodb-community-operator/pkg/util/:"
  ls -d mongodb-community-operator/pkg/util/*/ 2>/dev/null
  echo
  echo "mongodb-community-operator/pkg/kube/ (should be empty):"
  ls -d mongodb-community-operator/pkg/kube/*/ 2>/dev/null || echo "(directory empty or removed)"
}
```

Expected:
- 4 commits in the range (Chunk 1's service merge, Chunk 2's 5-util batch, Chunk 3's 7-kube batch, Chunk 4's 5-non-leaf batch). If Chunk 4 was split or extra fix-up commits were needed, the count may be slightly higher — that is fine.
- `mongodb-community-operator/pkg/kube/` is empty or removed.
- `mongodb-community-operator/pkg/util/` retains 6 subdirs: `functions, generate, result, state, status, versions`.

- [ ] **Step 8: No commit unless an issue was fixed**

If Steps 1–7 all passed cleanly, Plan 1 is complete. The branch `decuple-mdb-community` now carries the foundation: 18 shared packages relocated to root, one transient `pkg/util/merge → automationconfig` edge remaining, ready for Plan 2.

If any step required a fix-up, commit it with a clear message like `refactor: fix Plan-1 sweep miss in <path>` and re-run Steps 3–6 before declaring done.

- [ ] **Step 9: Hand off to the user**

Report:

- The 4 commit SHAs on `decuple-mdb-community` for Plan 1.
- The single documented residual (`pkg/util/merge → mongodb-community-operator/pkg/automationconfig`, 2 files).
- The total importer-update count (from `git diff --stat`) so the user has a sense of blast radius.
- Confirmation that no behaviour changed (the final test-diff was empty).

Wait for the user's go-ahead before starting Plan 2.

---

End of Chunk 5.

---

## Out-of-scope reminders for future plans

After Plan 1 lands, the following remain to be handled in subsequent plans (each its own document):

- **Plan 2** — Move `pkg/automationconfig` to root (clears the Plan-1 residual).
- **Plan 3** — Move `api/v1/common` to root.
- **Plan 4** — Move `pkg/agent`, `pkg/mongot`, `pkg/tls` to root.
- **Plan 5** — Cluster 8 residual (verify `controllers/searchcontroller` clean after Plans 1–4; apply depguard allowlist entry for `MongoDBCommunity`).
- **Plan 6** — Inline MCO `controllers` root references (2 symbols).
- **Plan 7** — Construct cluster (investigation + duplicate or move).
- **Plan 8** — Inline auth long tail.
- **Plan 9** — AppDB-embedded MCO types → shared `api/v1/common`.
- **Plan 10** — Enable `depguard` and configure the allowlist (the gate that locks in the boundary).

These plans are deliberately separate documents and are not part of this plan's review loop.

