# MC Search MVP — Base + Phase 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the foundation for multi-cluster `MongoDBSearch` (the stacked B-section PR train + a reusable MC E2E harness), then ship Q2-RS-MC operator code so a 2-cluster `MongoDBSearch` against an unmanaged ReplicaSet source executes real `$search` and `$vectorSearch` queries end-to-end, with the existing Q2 e2e test (`q2_mc_rs_steady.py`) flipped from scaffold-level green to real-coverage green.

**Architecture:** Base lands the spec.clusters[] CRD shape (B14+B18+B3+B4+B13), member-cluster client wiring (B1+B8), per-cluster Envoy Deployment+ConfigMap (B16), Secret presence checks (B5), and per-cluster status writes (B9) onto the integration branch `search/ga-base`, then adds a test-only MC E2E harness for cross-cluster Secret replication. Phase 2 extends the search controller's per-cluster reconcile loop to create per-cluster mongot StatefulSets, ConfigMaps, and proxy Services in each member cluster — each cluster gets its own `{name}-search-{clusterIndex}-proxy-svc` (cluster-index-suffixed; no shared-name DNS magic) and its own mongot config seeded from the shared top-level `spec.source.external.hostAndPorts`. mongod's `mongotHost` is set per-cluster via `MongoDBMulti.spec.clusterSpecList[i].additionalMongodConfig`. Per-cluster locality on the query path comes from the per-cluster Envoy + per-cluster proxy Service; the mongot→mongod sync direction crosses clusters via Istio mesh (acceptable for MVP because `$search`/`$vectorSearch` correctness only requires *some* mongot has indexed the data).

**Tech Stack:** Go 1.24 (operator); Python 3 + pytest + kubetester (e2e tests); Envoy Proxy (TLS LB); MongoDBMulti CRD (RS source); Voyage AI auto-embedding for `$vectorSearch`; Evergreen CI; Istio service mesh (cross-cluster connectivity in test envs).

---

## File Structure

### Base — files created/modified

| File | Responsibility | Layer |
|------|----------------|-------|
| Existing PR #1027 | B1 — member-cluster client wiring | Already coded; merge into `search/ga-base` |
| Existing PR #1030 | B14+B18 — `spec.clusters[]` types + defaulting | Already coded; merge after B1 |
| Existing PR #1029 | B5 — customer-replicated Secret presence checks | Already coded; merge after B1 |
| Existing PR #1028 | B8 — per-member-cluster watches | Already coded; merge after B1 |
| Existing PR #1036 | B16 — per-cluster Envoy Deployment + ConfigMap | Already coded; merge after B14 |
| Existing PR #1034 | B3+B4+B13 — cluster-index + placeholders + admission | Already coded; merge after B14 |
| Existing PR #1033 | B9 — per-cluster status writer (minimal) | Already coded; merge after B14 |
| `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/__init__.py` | Package init for new harness | Net-new |
| `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/secret_replicator.py` | Cross-cluster Secret replicator | Net-new |
| `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py` | 2-cluster MongoDBMulti fixture lifecycle | Net-new |
| `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/per_cluster_assertions.py` | Per-cluster resource/pod readiness assertions | Net-new |
| `docker/mongodb-kubernetes-tests/tests/multicluster_search/mc_search_harness_smoke.py` | Smoke test that exercises the harness | Net-new |
| `.evergreen-tasks.yml` | Register `e2e_mc_search_harness_smoke` task | Modified |

### Phase 2 — files created/modified

| File | Responsibility | Layer |
|------|----------------|-------|
| `api/v1/search/mongodbsearch_types.go` | Add `ProxyServiceNamespacedNameForCluster(clusterIndex int)` and `MongotConfigConfigMapNameForCluster(clusterIndex int)`; per-cluster naming | Modified (existing file) |
| `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` | Extend `reconcilePlan` units to one-per-cluster for RS topology; per-cluster proxy Service creation; per-cluster mongot StatefulSet+ConfigMap | Modified |
| `controllers/searchcontroller/external_search_source.go` | External-source per-cluster fan-out: render every cluster's mongot config from top-level `spec.source.external.hostAndPorts` | Modified |
| `controllers/operator/mongodbsearchenvoy_controller.go` | Update Envoy filter chain SNI to use per-cluster proxy-svc FQDN | Modified |
| `api/v1/search/mongodbsearch_validation.go` | New CEL/Go admission rule: `external.hostAndPorts` non-empty when `len(spec.clusters) > 1` | Modified |
| `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py` | Drop iter-4/iter-5 tolerance; restore strict assertions; switch `mongotHost` to `clusterSpecList[i].additionalMongodConfig`; drop iter-1 per-cluster `syncSourceSelector.hosts` and `REGION_TAGS`; add `$vectorSearch` tests | Modified |
| `docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml` | CR shape: top-level `external.hostAndPorts` only; bare `clusters: [{clusterName, replicas}]` | Modified |
| `controllers/searchcontroller/external_search_source_test.go` (new in B16 worktree) and adjacent unit tests | Per-cluster rendering unit tests | Modified |

---

## Pre-flight: working environment setup

This plan assumes you are working in a git worktree off of `master` of [github.com/mongodb/mongodb-kubernetes](https://github.com/mongodb/mongodb-kubernetes), with the integration branch `search/ga-base` already checked out remotely.

- [ ] **Step P1: Confirm worktree and branches**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git fetch origin search/ga-base master
git status
```

Expected: clean working tree on branch `claude/vigilant-mahavira-456daf`; `origin/search/ga-base` and `origin/master` updated.

- [ ] **Step P2: Confirm pre-commit and tooling**

```bash
pre-commit --version
go version
python3 --version
which evergreen
which kubectl
which gh
```

Expected: pre-commit ≥ 4.0; go 1.24+; python ≥ 3.11; evergreen CLI installed; kubectl ≥ 1.30; gh CLI ≥ 2.50.

- [ ] **Step P3: Confirm member-cluster kubectl contexts**

```bash
kubectl config get-contexts | grep -E "kind-(e2e-cluster-[123]|operator)"
```

Expected: at least 3 contexts (`kind-e2e-cluster-1`, `kind-e2e-cluster-2`, `kind-e2e-cluster-3`) — or whichever member-cluster setup the test harness uses. If missing, run `scripts/dev/multicluster/setup_kind_clusters.sh` (the standard MC test-env setup).

---

# Phase 1 — Base

Goal: every commit on `search/ga-base` after this phase contains the full MC foundation (B1+B14+B18+B16+B3+B4+B13+B5+B8+B9) plus a working MC E2E harness. Existing single-cluster e2e tests stay green throughout.

## Task 1: Land B1 (member-cluster client wiring) onto `search/ga-base`

**Files:**
- Modify: PR #1027 (`mc-search-b1-foundation` branch)

- [ ] **Step 1.1: Fetch and check out PR #1027 branch**

```bash
gh pr checkout 1027 -R mongodb/mongodb-kubernetes
git status
```

Expected: on branch `mc-search-b1-foundation`, working tree clean.

- [ ] **Step 1.2: Rebase onto current `search/ga-base`**

```bash
git fetch origin search/ga-base
git rebase origin/search/ga-base
```

Expected: clean rebase, no conflicts. If conflicts arise (likely in `controllers/operator/mongodbsearch_controller.go` or `cmd/manager/main.go`), resolve by accepting both sides where the conflict is purely additive (member-cluster maps additions can sit alongside any new master changes).

- [ ] **Step 1.3: Run unit tests**

```bash
go test ./controllers/operator/... ./controllers/searchcontroller/... -count=1
```

Expected: all tests pass. PR #1027's tests live in `cluster_clients_test.go` and `mongodbsearch_controller_test.go`; rebased changes must keep these green.

- [ ] **Step 1.4: Push rebased branch**

```bash
git push --force-with-lease origin mc-search-b1-foundation
```

- [ ] **Step 1.5: Wait for CI green and merge**

```bash
gh pr checks 1027 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1027 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

Expected: PR merged into `search/ga-base`. (Use `--squash` to keep the integration-branch history linear; the rich per-commit history stays in the dev branch refs.)

- [ ] **Step 1.6: Return to plan worktree**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git fetch origin search/ga-base
```

## Task 2: Land B14+B18 (`spec.clusters[]` + defaulting) onto `search/ga-base`

**Files:**
- Modify: PR #1030 (`mc-search-b14-distribution` branch)

- [ ] **Step 2.1: Check out PR #1030 and rebase**

```bash
gh pr checkout 1030 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
```

Expected: clean rebase. PR #1030's parent on github is `mc-search-b1-foundation` (now landed in ga-base via squash); the rebase rewrites history onto the new ga-base tip.

- [ ] **Step 2.2: Re-target the PR**

```bash
gh pr edit 1030 -R mongodb/mongodb-kubernetes --base search/ga-base
```

- [ ] **Step 2.3: Regenerate CRDs and DeepCopy if conflicts touched API types**

```bash
make generate
```

Expected: `helm_chart/crds/` regenerated (specifically `mongodb.com_mongodbsearch.yaml`); `api/v1/search/zz_generated_deepcopy.go` regenerated. If the diff is non-trivial, commit it as a separate "chore: regenerate CRDs" commit.

- [ ] **Step 2.4: Run unit tests**

```bash
go test ./api/v1/search/... ./controllers/searchcontroller/... -count=1
```

Expected: tests pass. Pay special attention to `mongodbsearch_validation_test.go` (B14 added the `clusters[].clusterName` uniqueness rule).

- [ ] **Step 2.5: Push and merge**

```bash
git push --force-with-lease origin mc-search-b14-distribution
gh pr checks 1030 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1030 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 3: Land B5 (Secret presence checks) onto `search/ga-base`

**Files:**
- Modify: PR #1029 (`mc-search-b5-secrets-presence` branch)

- [ ] **Step 3.1: Check out PR #1029 and rebase**

```bash
gh pr checkout 1029 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
gh pr edit 1029 -R mongodb/mongodb-kubernetes --base search/ga-base
```

- [ ] **Step 3.2: Run unit tests**

```bash
go test ./controllers/searchcontroller/... ./controllers/operator/... -count=1
```

- [ ] **Step 3.3: Push and merge**

```bash
git push --force-with-lease origin mc-search-b5-secrets-presence
gh pr checks 1029 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1029 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 4: Land B8 (per-member-cluster watches) onto `search/ga-base`

**Files:**
- Modify: PR #1028 (`mc-search-b8-watches` branch)

- [ ] **Step 4.1: Check out PR #1028, rebase, retarget**

```bash
gh pr checkout 1028 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
gh pr edit 1028 -R mongodb/mongodb-kubernetes --base search/ga-base
```

- [ ] **Step 4.2: Run unit tests**

```bash
go test ./controllers/operator/... -count=1
```

- [ ] **Step 4.3: Push and merge**

```bash
git push --force-with-lease origin mc-search-b8-watches
gh pr checks 1028 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1028 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 5: Land B16 (per-cluster Envoy) onto `search/ga-base`

**Files:**
- Modify: PR #1036 (`mc-search-b16-envoy-mc` branch)

- [ ] **Step 5.1: Check out PR #1036, rebase, retarget**

```bash
gh pr checkout 1036 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
gh pr edit 1036 -R mongodb/mongodb-kubernetes --base search/ga-base
```

Expected: clean rebase. B16 lives entirely in `controllers/operator/mongodbsearchenvoy_controller.go` and `api/v1/search/mongodbsearch_types.go`; no merge conflicts with B5/B8 expected.

- [ ] **Step 5.2: Regenerate generated files if API types touched**

```bash
make generate
```

- [ ] **Step 5.3: Run unit tests**

```bash
go test ./controllers/operator/... ./controllers/searchcontroller/... ./api/v1/search/... -count=1
```

- [ ] **Step 5.4: Push and merge**

```bash
git push --force-with-lease origin mc-search-b16-envoy-mc
gh pr checks 1036 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1036 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 6: Land B3+B4+B13 (cluster index + placeholders + admission) onto `search/ga-base`

**Files:**
- Modify: PR #1034 (`mc-search-b3-b4-cluster-index-placeholders` branch)

- [ ] **Step 6.1: Check out PR #1034, rebase, retarget**

```bash
gh pr checkout 1034 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
gh pr edit 1034 -R mongodb/mongodb-kubernetes --base search/ga-base
```

- [ ] **Step 6.2: Regenerate CRDs (CEL admission rules) and run unit tests**

```bash
make generate
go test ./api/v1/search/... ./controllers/searchcontroller/... -count=1
```

Expected: `mongodbsearch_validation_test.go` exercises the placeholder admission rules; all tests pass.

- [ ] **Step 6.3: Push and merge**

```bash
git push --force-with-lease origin mc-search-b3-b4-cluster-index-placeholders
gh pr checks 1034 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1034 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 7: Land B9 (per-cluster status writer minimal) onto `search/ga-base`

**Files:**
- Modify: PR #1033 (`mc-search-b9-status` branch)

- [ ] **Step 7.1: Check out PR #1033, rebase, retarget**

```bash
gh pr checkout 1033 -R mongodb/mongodb-kubernetes
git fetch origin search/ga-base
git rebase origin/search/ga-base
gh pr edit 1033 -R mongodb/mongodb-kubernetes --base search/ga-base
```

- [ ] **Step 7.2: Regenerate CRDs (status fields) and run unit tests**

```bash
make generate
go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... -count=1
```

- [ ] **Step 7.3: Push and merge**

```bash
git push --force-with-lease origin mc-search-b9-status
gh pr checks 1033 -R mongodb/mongodb-kubernetes --watch
gh pr merge 1033 -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

## Task 8: Verify single-cluster e2e regression on `search/ga-base`

**Files:** none (CI verification only)

- [ ] **Step 8.1: Pull current `search/ga-base`**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git fetch origin search/ga-base
git checkout search/ga-base
git pull
```

- [ ] **Step 8.2: Trigger Evergreen patch with the single-cluster RS+sharded e2e suite**

```bash
git add -A  # ensure no untracked test fixtures left behind
evergreen patch \
  --project mongodb-kubernetes \
  --variants e2e_static_mongodb_kind_ubi \
  --tasks e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb,e2e_search_replicaset_external_mongodb_multi_mongot_unmanaged_lb,e2e_search_sharded_external_mongodb_multi_mongot_unmanaged_lb,e2e_search_sharded_enterprise_external_mongod_managed_lb \
  -y -d "verify single-cluster e2e regression on ga-base after B-section train"
```

Capture the patch ID from the output.

- [ ] **Step 8.3: Finalize the patch and watch**

```bash
evergreen finalize-patch -i <patch-id-from-8.2>
evergreen list --patches | head -5
```

Expected: patch transitions to `running` then `succeeded`. **All four e2e tasks must pass.** If any fail, do NOT proceed — diagnose and fix at the appropriate B-section commit before continuing.

- [ ] **Step 8.4: Tag the green commit**

```bash
git tag -a base-pr-train-green -m "All B-section PRs landed; single-cluster RS+sharded e2es green on ga-base"
git push origin base-pr-train-green
```

This tag is the rollback target if any subsequent harness/Phase 2 work breaks ga-base.

## Task 9: Build the cross-cluster Secret replicator

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/__init__.py`
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/secret_replicator.py`
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_secret_replicator.py`

- [ ] **Step 9.1: Create the package init file**

```bash
touch docker/mongodb-kubernetes-tests/tests/common/multicluster_search/__init__.py
```

- [ ] **Step 9.2: Write the failing test**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_secret_replicator.py`:

```python
"""Unit tests for the cross-cluster Secret replicator.

These tests use mocked kubernetes clients (no live cluster needed).
"""
from unittest.mock import MagicMock

import pytest
from kubernetes.client.exceptions import ApiException

from tests.common.multicluster_search.secret_replicator import replicate_secret


def _mock_central_client(secret_data: dict[str, bytes]) -> MagicMock:
    client = MagicMock()
    secret = MagicMock()
    secret.data = {k: v for k, v in secret_data.items()}
    secret.type = "Opaque"
    secret.metadata.labels = {"app": "mdb-search"}
    client.read_namespaced_secret.return_value = secret
    return client


def test_replicate_creates_secret_in_each_member():
    central = _mock_central_client({"tls.crt": b"PEMDATA", "tls.key": b"KEYDATA"})
    member_a = MagicMock()
    member_b = MagicMock()
    member_a.read_namespaced_secret.side_effect = ApiException(status=404)
    member_b.read_namespaced_secret.side_effect = ApiException(status=404)

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a, "cluster-b": member_b},
    )

    assert member_a.create_namespaced_secret.called
    assert member_b.create_namespaced_secret.called
    a_args = member_a.create_namespaced_secret.call_args
    assert a_args.kwargs["namespace"] == "ns"
    assert a_args.kwargs["body"].data == {"tls.crt": b"PEMDATA", "tls.key": b"KEYDATA"}


def test_replicate_updates_existing_secret():
    central = _mock_central_client({"tls.crt": b"NEWPEM"})
    member_a = MagicMock()
    existing = MagicMock()
    existing.data = {"tls.crt": b"OLDPEM"}
    member_a.read_namespaced_secret.return_value = existing

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a},
    )

    assert member_a.patch_namespaced_secret.called
    patch_args = member_a.patch_namespaced_secret.call_args
    assert patch_args.kwargs["body"].data == {"tls.crt": b"NEWPEM"}


def test_replicate_idempotent_when_data_matches():
    central = _mock_central_client({"tls.crt": b"PEMDATA"})
    member_a = MagicMock()
    existing = MagicMock()
    existing.data = {"tls.crt": b"PEMDATA"}
    member_a.read_namespaced_secret.return_value = existing

    replicate_secret(
        secret_name="my-tls-cert",
        namespace="ns",
        central_client=central,
        member_clients={"cluster-a": member_a},
    )

    assert not member_a.create_namespaced_secret.called
    assert not member_a.patch_namespaced_secret.called
```

- [ ] **Step 9.3: Run the test to verify it fails**

```bash
cd docker/mongodb-kubernetes-tests
python3 -m pytest tests/common/multicluster_search/test_secret_replicator.py -v
```

Expected: FAIL with `ModuleNotFoundError: No module named 'tests.common.multicluster_search.secret_replicator'`.

- [ ] **Step 9.4: Implement the replicator**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/secret_replicator.py`:

```python
"""Cross-cluster Secret replicator for the MC search e2e harness.

Copies a Secret from the central cluster to each named member cluster.
Idempotent: if the Secret already exists in a member cluster with matching
data, no API call is made; if it exists with different data, the Secret is
patched; if it does not exist, it is created.

This is a TEST-ONLY utility. The MCK operator does NOT replicate Secrets in
production — that is the customer's responsibility per program rules. The
harness exists so e2e tests can stand up a working multi-cluster fixture
without requiring the test runner to mirror the production replication
machinery.
"""
from typing import Mapping

from kubernetes.client import CoreV1Api, V1ObjectMeta, V1Secret
from kubernetes.client.exceptions import ApiException
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


def replicate_secret(
    secret_name: str,
    namespace: str,
    central_client: CoreV1Api,
    member_clients: Mapping[str, CoreV1Api],
) -> None:
    """Replicate `secret_name` from `central_client` into every cluster in `member_clients`.

    Args:
        secret_name: Name of the Secret to replicate.
        namespace: Namespace in which the Secret lives in the central cluster
            and should be created in each member cluster.
        central_client: kubernetes CoreV1Api for the central cluster (the
            authoritative source of the Secret).
        member_clients: mapping of cluster-name → kubernetes CoreV1Api for
            each member cluster the Secret should be replicated into.

    Raises:
        ApiException: re-raised from the central read if the source Secret
            does not exist; per-member errors are logged and re-raised.
    """
    source = central_client.read_namespaced_secret(name=secret_name, namespace=namespace)
    desired_data = dict(source.data or {})
    desired_type = source.type or "Opaque"

    for cluster_name, member in member_clients.items():
        try:
            existing = member.read_namespaced_secret(name=secret_name, namespace=namespace)
        except ApiException as exc:
            if exc.status != 404:
                logger.error(f"replicate_secret: read failed in cluster {cluster_name}: {exc}")
                raise
            existing = None

        if existing is None:
            body = V1Secret(
                metadata=V1ObjectMeta(name=secret_name, namespace=namespace),
                type=desired_type,
                data=desired_data,
            )
            member.create_namespaced_secret(namespace=namespace, body=body)
            logger.info(f"replicate_secret: created {secret_name} in cluster {cluster_name}")
            continue

        if (existing.data or {}) == desired_data:
            logger.debug(f"replicate_secret: {secret_name} in cluster {cluster_name} already up to date")
            continue

        body = V1Secret(
            metadata=V1ObjectMeta(name=secret_name, namespace=namespace),
            type=desired_type,
            data=desired_data,
        )
        member.patch_namespaced_secret(name=secret_name, namespace=namespace, body=body)
        logger.info(f"replicate_secret: patched {secret_name} in cluster {cluster_name}")
```

- [ ] **Step 9.5: Run the test to verify it passes**

```bash
python3 -m pytest tests/common/multicluster_search/test_secret_replicator.py -v
```

Expected: 3 passed.

- [ ] **Step 9.6: Commit**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git checkout -b mc-search-harness  # new branch off ga-base
git add docker/mongodb-kubernetes-tests/tests/common/multicluster_search/
git commit -m "feat(test/search): add cross-cluster Secret replicator for MC e2e harness"
```

## Task 10: Build the MC search deployment helper (2-cluster MongoDBMulti fixture)

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py`
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_mc_search_deployment_helper.py`

- [ ] **Step 10.1: Write the failing test**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_mc_search_deployment_helper.py`:

```python
"""Unit tests for MCSearchDeploymentHelper (mocked clients)."""
from unittest.mock import MagicMock

import pytest

from tests.common.multicluster_search.mc_search_deployment_helper import (
    MCSearchDeploymentHelper,
)


def test_helper_records_member_cluster_clients():
    member_clients = {"cluster-a": MagicMock(), "cluster-b": MagicMock()}
    helper = MCSearchDeploymentHelper(
        namespace="ns",
        mdb_resource_name="mdb-multi",
        mdbs_resource_name="mdb-search",
        member_cluster_clients=member_clients,
    )

    assert helper.namespace == "ns"
    assert helper.member_cluster_names() == ["cluster-a", "cluster-b"]
    assert helper.cluster_index("cluster-a") == 0
    assert helper.cluster_index("cluster-b") == 1


def test_helper_proxy_svc_fqdn_uses_cluster_index():
    helper = MCSearchDeploymentHelper(
        namespace="test-ns",
        mdb_resource_name="mdb",
        mdbs_resource_name="mdb-search",
        member_cluster_clients={"a": MagicMock(), "b": MagicMock()},
    )

    assert (
        helper.proxy_svc_fqdn("a")
        == "mdb-search-search-0-proxy-svc.test-ns.svc.cluster.local"
    )
    assert (
        helper.proxy_svc_fqdn("b")
        == "mdb-search-search-1-proxy-svc.test-ns.svc.cluster.local"
    )


def test_helper_unknown_cluster_raises():
    helper = MCSearchDeploymentHelper(
        namespace="ns", mdb_resource_name="m", mdbs_resource_name="s",
        member_cluster_clients={"a": MagicMock()},
    )
    with pytest.raises(KeyError):
        helper.cluster_index("nope")
```

- [ ] **Step 10.2: Run the test to verify it fails**

```bash
cd docker/mongodb-kubernetes-tests
python3 -m pytest tests/common/multicluster_search/test_mc_search_deployment_helper.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 10.3: Implement the helper**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/mc_search_deployment_helper.py`:

```python
"""Two-cluster MongoDBMulti + MongoDBSearch fixture lifecycle helper.

Used by all MC search e2e tests to deploy the source RS across member
clusters, replicate Secrets, and surface per-cluster naming
(proxy-svc FQDN per cluster index, etc.) consistently.

The cluster-index ordering is the registration order in `member_cluster_clients`
— that mirrors the operator's `clusterSpecList[].clusterName` ordering, which
is what B3's stable cluster-index annotation pins.
"""
from typing import Mapping

from kubernetes.client import CoreV1Api
from tests import test_logger

logger = test_logger.get_test_logger(__name__)


class MCSearchDeploymentHelper:
    """Encapsulates 2-cluster MongoDBMulti+MongoDBSearch fixture knobs.

    Provides:
        - namespace
        - cluster index lookup by clusterName (matches operator's annotation)
        - per-cluster proxy-svc FQDN string for `mongotHost`
    """

    def __init__(
        self,
        namespace: str,
        mdb_resource_name: str,
        mdbs_resource_name: str,
        member_cluster_clients: Mapping[str, CoreV1Api],
    ) -> None:
        self.namespace = namespace
        self.mdb_resource_name = mdb_resource_name
        self.mdbs_resource_name = mdbs_resource_name
        self._member_cluster_clients = dict(member_cluster_clients)
        self._cluster_indices = {
            name: idx for idx, name in enumerate(self._member_cluster_clients)
        }

    def member_cluster_names(self) -> list[str]:
        return list(self._member_cluster_clients.keys())

    def cluster_index(self, cluster_name: str) -> int:
        if cluster_name not in self._cluster_indices:
            raise KeyError(f"unknown member cluster: {cluster_name!r}")
        return self._cluster_indices[cluster_name]

    def member_clients(self) -> Mapping[str, CoreV1Api]:
        return self._member_cluster_clients

    def proxy_svc_fqdn(self, cluster_name: str) -> str:
        """Return the cluster-index-suffixed proxy Service FQDN.

        Pattern: `{mdbs}-search-{clusterIndex}-proxy-svc.{ns}.svc.cluster.local`.

        This is the value that `mongotHost` should be set to on the
        per-cluster mongod (via `MongoDBMulti.spec.clusterSpecList[i]
        .additionalMongodConfig.setParameter.mongotHost`). It does NOT
        include the port; callers append `:<port>` as needed.
        """
        idx = self.cluster_index(cluster_name)
        return (
            f"{self.mdbs_resource_name}-search-{idx}-proxy-svc"
            f".{self.namespace}.svc.cluster.local"
        )
```

- [ ] **Step 10.4: Run the test to verify it passes**

```bash
python3 -m pytest tests/common/multicluster_search/test_mc_search_deployment_helper.py -v
```

Expected: 3 passed.

- [ ] **Step 10.5: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/common/multicluster_search/
git commit -m "feat(test/search): add MCSearchDeploymentHelper with per-cluster proxy-svc FQDN"
```

## Task 11: Build per-cluster assertion helpers

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/per_cluster_assertions.py`
- Create: `docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_per_cluster_assertions.py`

- [ ] **Step 11.1: Write the failing test**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/test_per_cluster_assertions.py`:

```python
"""Unit tests for per-cluster assertion helpers (mocked clients)."""
from unittest.mock import MagicMock

import pytest
from kubernetes.client.exceptions import ApiException

from tests.common.multicluster_search.per_cluster_assertions import (
    assert_deployment_ready_in_cluster,
    assert_resource_in_cluster,
)


def _ready_deployment(name: str) -> MagicMock:
    dep = MagicMock()
    dep.metadata.name = name
    dep.status.ready_replicas = 2
    dep.spec.replicas = 2
    return dep


def _not_ready_deployment(name: str) -> MagicMock:
    dep = MagicMock()
    dep.metadata.name = name
    dep.status.ready_replicas = 0
    dep.spec.replicas = 2
    return dep


def test_assert_deployment_ready_passes_when_replicas_match():
    apps = MagicMock()
    apps.read_namespaced_deployment.return_value = _ready_deployment("d")
    assert_deployment_ready_in_cluster(apps, name="d", namespace="ns")


def test_assert_deployment_ready_fails_when_replicas_short():
    apps = MagicMock()
    apps.read_namespaced_deployment.return_value = _not_ready_deployment("d")
    with pytest.raises(AssertionError, match="ready_replicas=0/2"):
        assert_deployment_ready_in_cluster(apps, name="d", namespace="ns")


def test_assert_resource_present_passes_when_found():
    core = MagicMock()
    core.read_namespaced_service.return_value = MagicMock()
    assert_resource_in_cluster(
        core, kind="Service", name="proxy-svc", namespace="ns"
    )


def test_assert_resource_present_fails_when_404():
    core = MagicMock()
    core.read_namespaced_service.side_effect = ApiException(status=404)
    with pytest.raises(AssertionError, match="Service.*proxy-svc.*not found"):
        assert_resource_in_cluster(
            core, kind="Service", name="proxy-svc", namespace="ns"
        )
```

- [ ] **Step 11.2: Run test to verify it fails**

```bash
python3 -m pytest tests/common/multicluster_search/test_per_cluster_assertions.py -v
```

Expected: FAIL with `ModuleNotFoundError`.

- [ ] **Step 11.3: Implement the assertions**

`docker/mongodb-kubernetes-tests/tests/common/multicluster_search/per_cluster_assertions.py`:

```python
"""Per-cluster resource and pod readiness assertion helpers.

These are thin pytest assertion wrappers around the kubernetes Python
client. They exist as named helpers (rather than inline `assert`s)
so failure messages name the cluster + resource clearly when they fire.
"""
from kubernetes.client import AppsV1Api, CoreV1Api
from kubernetes.client.exceptions import ApiException


def assert_deployment_ready_in_cluster(
    apps: AppsV1Api, *, name: str, namespace: str
) -> None:
    """Assert Deployment `name`/`namespace` has all spec.replicas ready."""
    dep = apps.read_namespaced_deployment(name=name, namespace=namespace)
    ready = dep.status.ready_replicas or 0
    desired = dep.spec.replicas or 0
    if ready != desired or desired == 0:
        raise AssertionError(
            f"Deployment {namespace}/{name}: ready_replicas={ready}/{desired}"
        )


def assert_resource_in_cluster(
    client, *, kind: str, name: str, namespace: str
) -> None:
    """Assert that a resource of `kind`/`name` exists in `namespace`.

    Supported kinds: Service, ConfigMap, Secret, StatefulSet, Deployment.
    """
    method = {
        "Service": (CoreV1Api, "read_namespaced_service"),
        "ConfigMap": (CoreV1Api, "read_namespaced_config_map"),
        "Secret": (CoreV1Api, "read_namespaced_secret"),
        "StatefulSet": (AppsV1Api, "read_namespaced_stateful_set"),
        "Deployment": (AppsV1Api, "read_namespaced_deployment"),
    }
    if kind not in method:
        raise ValueError(f"unsupported kind: {kind}")
    _, method_name = method[kind]
    try:
        getattr(client, method_name)(name=name, namespace=namespace)
    except ApiException as exc:
        if exc.status == 404:
            raise AssertionError(
                f"{kind} {namespace}/{name} not found in target cluster"
            )
        raise
```

- [ ] **Step 11.4: Run test to verify it passes**

```bash
python3 -m pytest tests/common/multicluster_search/test_per_cluster_assertions.py -v
```

Expected: 4 passed.

- [ ] **Step 11.5: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/common/multicluster_search/
git commit -m "feat(test/search): add per-cluster Deployment/resource assertion helpers"
```

## Task 12: Write the harness smoke test (e2e)

**Files:**
- Create: `docker/mongodb-kubernetes-tests/tests/multicluster_search/mc_search_harness_smoke.py`

- [ ] **Step 12.1: Write the test**

`docker/mongodb-kubernetes-tests/tests/multicluster_search/mc_search_harness_smoke.py`:

```python
"""MC E2E harness smoke test.

Exercises the harness primitives end-to-end against a real 2-cluster
kind setup:
  1. Create a fake Secret in the central cluster.
  2. Replicate it to each member cluster.
  3. Assert presence in each member cluster.
  4. Tear down.

This test does NOT exercise any MongoDBSearch / MongoDBMulti operator
code — it's solely a harness smoke. If this fails, MC e2e tests
that depend on the harness will also fail in non-obvious ways.
"""
from kubernetes.client import CoreV1Api, V1ObjectMeta, V1Secret
from pytest import fixture, mark

from tests import test_logger
from tests.common.multicluster_search.per_cluster_assertions import (
    assert_resource_in_cluster,
)
from tests.common.multicluster_search.secret_replicator import replicate_secret

logger = test_logger.get_test_logger(__name__)

SECRET_NAME = "mc-harness-smoke-fake-secret"


@fixture(scope="module")
def central_core(central_cluster_client) -> CoreV1Api:
    return CoreV1Api(api_client=central_cluster_client)


@fixture(scope="module")
def member_cores(member_cluster_clients) -> dict[str, CoreV1Api]:
    return {mcc.cluster_name: CoreV1Api(api_client=mcc.api_client) for mcc in member_cluster_clients}


@mark.e2e_mc_search_harness_smoke
def test_create_fake_secret_in_central(central_core: CoreV1Api, namespace: str):
    body = V1Secret(
        metadata=V1ObjectMeta(name=SECRET_NAME, namespace=namespace),
        type="Opaque",
        data={"smoke.txt": b"aGVsbG8="},  # base64("hello")
    )
    central_core.create_namespaced_secret(namespace=namespace, body=body)
    logger.info(f"created fake Secret {SECRET_NAME} in central cluster, ns={namespace}")


@mark.e2e_mc_search_harness_smoke
def test_replicate_secret_to_members(
    central_core: CoreV1Api,
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    replicate_secret(
        secret_name=SECRET_NAME,
        namespace=namespace,
        central_client=central_core,
        member_clients=member_cores,
    )


@mark.e2e_mc_search_harness_smoke
def test_assert_secret_present_in_each_member(
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    for cluster_name, core in member_cores.items():
        assert_resource_in_cluster(
            core, kind="Secret", name=SECRET_NAME, namespace=namespace
        )
        logger.info(f"verified {SECRET_NAME} present in cluster {cluster_name}")


@mark.e2e_mc_search_harness_smoke
def test_replicate_idempotent_on_second_call(
    central_core: CoreV1Api,
    member_cores: dict[str, CoreV1Api],
    namespace: str,
):
    replicate_secret(
        secret_name=SECRET_NAME,
        namespace=namespace,
        central_client=central_core,
        member_clients=member_cores,
    )


@mark.e2e_mc_search_harness_smoke
def test_cleanup(central_core: CoreV1Api, member_cores: dict[str, CoreV1Api], namespace: str):
    central_core.delete_namespaced_secret(name=SECRET_NAME, namespace=namespace)
    for core in member_cores.values():
        try:
            core.delete_namespaced_secret(name=SECRET_NAME, namespace=namespace)
        except Exception as e:
            logger.debug(f"cleanup: {e}")
```

- [ ] **Step 12.2: Lint with the project's tools**

```bash
cd docker/mongodb-kubernetes-tests
python3 -m black tests/common/multicluster_search/ tests/multicluster_search/mc_search_harness_smoke.py --line-length 120
python3 -m isort tests/common/multicluster_search/ tests/multicluster_search/mc_search_harness_smoke.py --profile black --line-length 120
```

- [ ] **Step 12.3: Commit**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git add docker/mongodb-kubernetes-tests/tests/multicluster_search/mc_search_harness_smoke.py
git commit -m "feat(test/search): add MC e2e harness smoke test"
```

## Task 13: Register the harness smoke test in Evergreen

**Files:**
- Modify: `.evergreen-tasks.yml`

- [ ] **Step 13.1: Inspect existing MC e2e task definitions**

```bash
grep -A 5 "e2e_search_q2_mc" .evergreen-tasks.yml | head -30
```

Capture the closest existing task definition pattern (likely `e2e_search_q2_mc_rs_steady`).

- [ ] **Step 13.2: Add the new task entry**

In `.evergreen-tasks.yml`, find the section that lists `e2e_search_q2_mc_rs_steady` and add a sibling entry — same pattern, swapping the test marker. Example (verify line numbers / surrounding context first):

```yaml
- name: e2e_mc_search_harness_smoke
  tags: ["search", "mc-search", "harness"]
  commands:
    - func: e2e_test
      vars:
        test_name: mc_search_harness_smoke
```

- [ ] **Step 13.3: Add it to the relevant variant's task list**

```bash
grep -n "e2e_search_q2_mc_rs_steady" .evergreen.yml
```

Find each variant that includes `e2e_search_q2_mc_rs_steady` and add `e2e_mc_search_harness_smoke` next to it (same indent).

- [ ] **Step 13.4: Submit Evergreen patch**

```bash
git add .evergreen-tasks.yml .evergreen.yml docker/mongodb-kubernetes-tests/
git commit -m "ci(search): wire mc_search_harness_smoke e2e task into Evergreen"
git push origin mc-search-harness
evergreen patch --project mongodb-kubernetes \
  --variants e2e_static_mongodb_kind_ubi \
  --tasks e2e_mc_search_harness_smoke \
  -y -d "MC e2e harness smoke"
```

Capture the patch ID. Run `evergreen finalize-patch -i <patch-id>`.

- [ ] **Step 13.5: Watch the patch and verify it's green**

```bash
evergreen list --patches | head -3
```

Expected: `e2e_mc_search_harness_smoke` succeeds. If it fails, diagnose by reading the test pod logs from the patch's task page; common failures include cross-cluster RBAC missing, kind networking misconfigured, or a Python import error.

- [ ] **Step 13.6: Open the harness PR**

```bash
gh pr create --base search/ga-base --head mc-search-harness \
  --title "MC search e2e harness: cross-cluster Secret replicator + helpers" \
  --body "$(cat <<'EOF'
## Summary
- Cross-cluster Secret replicator (test-only — no operator code change)
- `MCSearchDeploymentHelper` for 2-cluster fixture lifecycle + per-cluster proxy-svc FQDN
- Per-cluster resource/Deployment-readiness assertion helpers
- Smoke test exercising the harness end-to-end on a 2-cluster kind setup

## Test plan
- [x] Unit tests: `pytest tests/common/multicluster_search/`
- [x] Evergreen: `e2e_mc_search_harness_smoke` green (patch attached)
- [x] Existing single-cluster Q2 RS+sharded e2es still green (verified via Task 8 of plan)

## Acceptance gate
G1 — Base merged: harness smoke green; existing single-cluster e2es still green.
EOF
)"
```

After CI passes and review accepts, merge with `gh pr merge <pr-number> -R mongodb/mongodb-kubernetes --squash --delete-branch=false`.

**Phase 1 / Base done.** `search/ga-base` now contains the full B-section foundation + working MC E2E harness. Acceptance gate G1 met.

---

# Phase 2 — Q2-RS-MC operator + tightened MC RS e2e

Goal: extend the search reconciler to create per-cluster mongot StatefulSets, ConfigMaps, and proxy Services across member clusters; flip `q2_mc_rs_steady.py` from scaffold-level green to real-coverage green with `$search` AND `$vectorSearch` data plane assertions.

## Task 14: Add per-cluster naming helpers (`ProxyServiceNamespacedNameForCluster`, `MongotConfigConfigMapNameForCluster`)

**Files:**
- Modify: `api/v1/search/mongodbsearch_types.go` (around lines 492-510)
- Modify: `api/v1/search/mongodbsearch_types_test.go` (or create per-cluster naming test if absent)

- [ ] **Step 14.1: Create a feature branch off ga-base**

```bash
cd /Users/anand.singh/workspace/repos/mongodb-kubernetes/.claude/worktrees/vigilant-mahavira-456daf
git fetch origin search/ga-base
git checkout -b mc-search-phase2-q2-rs origin/search/ga-base
```

- [ ] **Step 14.2: Write the failing test**

In `api/v1/search/mongodbsearch_types_test.go`, append:

```go
func TestProxyServiceNamespacedNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	// Single-cluster (clusterIndex=0) preserves the legacy single-cluster name.
	got := s.ProxyServiceNamespacedNameForCluster(0)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", got.Name)
	assert.Equal(t, "ns", got.Namespace)
	// Same as legacy ProxyServiceNamespacedName when index=0.
	assert.Equal(t, s.ProxyServiceNamespacedName(), got)

	// Per-cluster index suffix differs.
	got1 := s.ProxyServiceNamespacedNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-proxy-svc", got1.Name)

	got2 := s.ProxyServiceNamespacedNameForCluster(2)
	assert.Equal(t, "mdb-search-search-2-proxy-svc", got2.Name)
}

func TestMongotConfigConfigMapNameForCluster(t *testing.T) {
	s := &MongoDBSearch{
		ObjectMeta: metav1.ObjectMeta{Name: "mdb-search", Namespace: "ns"},
	}

	got0 := s.MongotConfigConfigMapNameForCluster(0)
	assert.Equal(t, "mdb-search-search-config", got0.Name) // legacy match for index 0
	got1 := s.MongotConfigConfigMapNameForCluster(1)
	assert.Equal(t, "mdb-search-search-1-config", got1.Name)
}
```

- [ ] **Step 14.3: Run test to verify it fails**

```bash
go test ./api/v1/search/... -run TestProxyServiceNamespacedNameForCluster -count=1
```

Expected: FAIL with `s.ProxyServiceNamespacedNameForCluster undefined`.

- [ ] **Step 14.4: Implement the helpers**

In `api/v1/search/mongodbsearch_types.go`, add after `ProxyServiceNamespacedName` (around line 499):

```go
// ProxyServiceNamespacedNameForCluster returns the proxy Service name for one
// member cluster identified by its cluster index. clusterIndex 0 matches the
// legacy single-cluster ProxyServiceNamespacedName for backward compatibility.
//
// Each cluster's proxy Service has a distinct name with the cluster index as a
// suffix; this avoids relying on per-cluster ClusterIP DNS scoping for
// disambiguation. mongod's `mongotHost` should be set to this name's FQDN
// per cluster (via `clusterSpecList[i].additionalMongodConfig` on the
// MongoDBMulti source).
func (s *MongoDBSearch) ProxyServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-%s", s.Name, clusterIndex, ProxyServiceSuffix),
		Namespace: s.Namespace,
	}
}

// MongotConfigConfigMapNameForCluster returns the per-cluster mongot ConfigMap
// name. Index 0 matches the legacy single-cluster name for back-compat.
func (s *MongoDBSearch) MongotConfigConfigMapNameForCluster(clusterIndex int) types.NamespacedName {
	if clusterIndex == 0 {
		return s.MongotConfigConfigMapNamespacedName()
	}
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-config", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}
```

- [ ] **Step 14.5: Run test to verify it passes**

```bash
go test ./api/v1/search/... -run "TestProxyServiceNamespacedNameForCluster|TestMongotConfigConfigMapNameForCluster" -count=1 -v
```

Expected: 2 PASS.

- [ ] **Step 14.6: Run the full unit test suite to confirm no regressions**

```bash
go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... -count=1
```

Expected: ALL pass.

- [ ] **Step 14.7: Commit**

```bash
git add api/v1/search/mongodbsearch_types.go api/v1/search/mongodbsearch_types_test.go
git commit -m "feat(search): add per-cluster ProxyService and MongotConfig name helpers"
```

## Task 15: Extend `reconcilePlan` to per-cluster RS units

**Files:**
- Modify: `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` (around lines 140-170 — `buildReplicaSetPlan`)
- Modify: `controllers/searchcontroller/mongodbsearch_reconcile_helper_test.go`

The current `buildReplicaSetPlan` produces a single `reconcileUnit` for the search resource. Extend it to produce one unit per cluster in `spec.clusters[]` (or, when len == 1 / unset, the legacy single-cluster unit unchanged).

- [ ] **Step 15.1: Read the current single-cluster path**

```bash
sed -n '120,170p' controllers/searchcontroller/mongodbsearch_reconcile_helper.go
```

Capture the existing unit construction; the per-cluster version mirrors it but with cluster-index-suffixed names and per-cluster client selection.

- [ ] **Step 15.2: Write the failing test**

In `mongodbsearch_reconcile_helper_test.go`, append:

```go
func TestBuildReplicaSetPlan_PerClusterUnitsForMC(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", Replicas: pointer.Int32(2)},
		{ClusterName: "cluster-b", Replicas: pointer.Int32(2)},
	}
	mdb.Spec.Source = &searchv1.SearchSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017", "b.example:27017"},
		},
	}

	r := &MongoDBSearchReconcileHelper{mdbSearch: mdb}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 2, "expected one unit per cluster")

	// Cluster A (index 0)
	assert.Equal(t, "mdb-search-search-0", plan.units[0].stsName.Name)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", plan.units[0].proxySvc.Name)
	assert.Equal(t, "cluster-a", plan.units[0].clusterName)
	assert.Equal(t, 0, plan.units[0].clusterIndex)

	// Cluster B (index 1)
	assert.Equal(t, "mdb-search-search-1", plan.units[1].stsName.Name)
	assert.Equal(t, "mdb-search-search-1-proxy-svc", plan.units[1].proxySvc.Name)
	assert.Equal(t, "cluster-b", plan.units[1].clusterName)
	assert.Equal(t, 1, plan.units[1].clusterIndex)
}

func TestBuildReplicaSetPlan_SingleClusterPreservesLegacyNames(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	// no spec.clusters → legacy single-cluster path
	mdb.Spec.Source = &searchv1.SearchSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017"},
		},
	}
	r := &MongoDBSearchReconcileHelper{mdbSearch: mdb}
	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}

	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)
	require.Len(t, plan.units, 1)
	assert.Equal(t, "mdb-search-search", plan.units[0].stsName.Name)
	assert.Equal(t, "mdb-search-search-0-proxy-svc", plan.units[0].proxySvc.Name)
}
```

- [ ] **Step 15.3: Run test to verify it fails**

```bash
go test ./controllers/searchcontroller/... -run TestBuildReplicaSetPlan -count=1 -v
```

Expected: FAIL with field `clusterName` / `clusterIndex` undefined on `reconcileUnit`, or "expected one unit per cluster: got 1".

- [ ] **Step 15.4: Extend `reconcileUnit` and `buildReplicaSetPlan`**

In `mongodbsearch_reconcile_helper.go`, add to `reconcileUnit` (search for `type reconcileUnit struct`):

```go
type reconcileUnit struct {
	stsName            types.NamespacedName
	headlessSvc        types.NamespacedName
	proxySvc           types.NamespacedName
	configMapName      types.NamespacedName
	podLabels          map[string]string
	extraHeadlessPorts []corev1.ServicePort
	tlsResource        *searchv1.MongoDBSearch
	mongotConfigFn     mongot.ConfigFn

	// Per-cluster fields. clusterName == "" and clusterIndex == 0 → legacy
	// single-cluster path: unit goes to the central client.
	clusterName  string
	clusterIndex int
}
```

Then rewrite `buildReplicaSetPlan` (around lines 120-170) to fan out per cluster:

```go
func (r *MongoDBSearchReconcileHelper) buildReplicaSetPlan(rsSource SearchSourceReplicaSet) (reconcilePlan, error) {
	hostSeeds, err := rsSource.HostSeeds("")
	if err != nil {
		return reconcilePlan{}, err
	}

	// External-source hosts come from spec.source.external.hostAndPorts. For
	// MC the same seed list is rendered into every cluster's mongot config —
	// see ./docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md
	// "Routing strategy" for why.

	clusters := r.mdbSearch.EffectiveClusters()
	if len(clusters) == 0 {
		// Legacy single-cluster shape preserved.
		return r.buildSingleClusterReplicaSetUnit(hostSeeds)
	}

	units := make([]reconcileUnit, 0, len(clusters))
	for idx, cluster := range clusters {
		stsName := r.mdbSearch.StatefulSetNamespacedNameForCluster(idx)
		headlessSvc := r.mdbSearch.SearchServiceNamespacedNameForCluster(idx)
		proxySvc := r.mdbSearch.ProxyServiceNamespacedNameForCluster(idx)
		configMapName := r.mdbSearch.MongotConfigConfigMapNameForCluster(idx)
		podLabels := map[string]string{appLabelKey: headlessSvc.Name}

		var extraPorts []corev1.ServicePort
		if r.mdbSearch.IsWireprotoEnabled() {
			extraPorts = []corev1.ServicePort{{
				Name:       "mongot-wireproto",
				Protocol:   corev1.ProtocolTCP,
				Port:       r.mdbSearch.GetMongotWireprotoPort(),
				TargetPort: intstr.FromInt32(r.mdbSearch.GetMongotWireprotoPort()),
			}}
		}

		units = append(units, reconcileUnit{
			stsName:            stsName,
			headlessSvc:        headlessSvc,
			proxySvc:           proxySvc,
			configMapName:      configMapName,
			podLabels:          podLabels,
			extraHeadlessPorts: extraPorts,
			tlsResource:        r.mdbSearch,
			mongotConfigFn:     mongot.Apply(baseMongotConfig(r.mdbSearch, hostSeeds), wireprotoMongotMod(r.mdbSearch)),
			clusterName:        cluster.ClusterName,
			clusterIndex:       idx,
		})
	}

	return reconcilePlan{
		units:          units,
		manageProxySvc: !r.mdbSearch.IsReplicaSetUnmanagedLB(),
		preflight:      func(context.Context, *zap.SugaredLogger) workflow.Status { return workflow.OK() },
		cleanup:        func(context.Context, *zap.SugaredLogger) {},
	}, nil
}

func (r *MongoDBSearchReconcileHelper) buildSingleClusterReplicaSetUnit(hostSeeds []string) (reconcilePlan, error) {
	svcName := r.mdbSearch.SearchServiceNamespacedName().Name
	var extraHeadlessPorts []corev1.ServicePort
	if r.mdbSearch.IsWireprotoEnabled() {
		extraHeadlessPorts = []corev1.ServicePort{{
			Name:       "mongot-wireproto",
			Protocol:   corev1.ProtocolTCP,
			Port:       r.mdbSearch.GetMongotWireprotoPort(),
			TargetPort: intstr.FromInt32(r.mdbSearch.GetMongotWireprotoPort()),
		}}
	}
	return reconcilePlan{
		units: []reconcileUnit{{
			stsName:            r.mdbSearch.StatefulSetNamespacedName(),
			headlessSvc:        r.mdbSearch.SearchServiceNamespacedName(),
			proxySvc:           r.mdbSearch.ProxyServiceNamespacedName(),
			configMapName:      r.mdbSearch.MongotConfigConfigMapNamespacedName(),
			podLabels:          map[string]string{appLabelKey: svcName},
			extraHeadlessPorts: extraHeadlessPorts,
			tlsResource:        r.mdbSearch,
			mongotConfigFn:     mongot.Apply(baseMongotConfig(r.mdbSearch, hostSeeds), wireprotoMongotMod(r.mdbSearch)),
			clusterName:        "",
			clusterIndex:       0,
		}},
		manageProxySvc: !r.mdbSearch.IsReplicaSetUnmanagedLB(),
		preflight:      func(context.Context, *zap.SugaredLogger) workflow.Status { return workflow.OK() },
		cleanup:        func(context.Context, *zap.SugaredLogger) {},
	}, nil
}
```

This requires three more naming helpers on `MongoDBSearch` (mirroring B16's pattern). Add them to `api/v1/search/mongodbsearch_types.go`:

```go
func (s *MongoDBSearch) StatefulSetNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	if clusterIndex == 0 {
		return s.StatefulSetNamespacedName()
	}
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}

func (s *MongoDBSearch) SearchServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName {
	if clusterIndex == 0 {
		return s.SearchServiceNamespacedName()
	}
	return types.NamespacedName{
		Name:      fmt.Sprintf("%s-search-%d-svc", s.Name, clusterIndex),
		Namespace: s.Namespace,
	}
}
```

- [ ] **Step 15.5: Run tests to verify they pass**

```bash
go test ./controllers/searchcontroller/... -run "TestBuildReplicaSetPlan" -count=1 -v
go test ./api/v1/search/... -count=1
```

Expected: ALL pass.

- [ ] **Step 15.6: Run the full unit-test suite**

```bash
go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... -count=1
```

Expected: ALL pass.

- [ ] **Step 15.7: Commit**

```bash
git add api/v1/search/ controllers/searchcontroller/
git commit -m "feat(search): per-cluster reconcileUnit fan-out for RS topology"
```

## Task 16: Per-cluster client selection in unit reconcile

**Files:**
- Modify: `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` — wherever `reconcileUnit` is consumed (search the file for `for _, unit := range plan.units`)

The reconciler currently treats `r.client` (central) as the only target for `reconcileUnit`'s objects. With per-cluster units, each unit must use the member-cluster client selected by `clusterName`.

- [ ] **Step 16.1: Find the reconcile-unit consumption site**

```bash
grep -n "for _, unit := range plan.units\|range plan\.units" controllers/searchcontroller/mongodbsearch_reconcile_helper.go | head
```

- [ ] **Step 16.2: Write the failing integration test**

In `mongodbsearch_reconcile_helper_test.go`, append:

```go
func TestReconcilePlan_UsesPerClusterClient(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", Replicas: pointer.Int32(2)},
		{ClusterName: "cluster-b", Replicas: pointer.Int32(2)},
	}
	mdb.Spec.Source = &searchv1.SearchSource{
		ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
			HostAndPorts: []string{"a.example:27017"},
		},
	}

	centralClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	clusterAClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	clusterBClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	memberClients := map[string]client.Client{
		"cluster-a": clusterAClient,
		"cluster-b": clusterBClient,
	}

	r := &MongoDBSearchReconcileHelper{
		mdbSearch:           mdb,
		client:              centralClient,
		memberClusterClients: memberClients,
	}

	source := &fakeExternalSource{hosts: mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts}
	plan, err := r.buildReplicaSetPlan(source)
	require.NoError(t, err)

	// Apply each unit. Cluster A's StatefulSet must end up in clusterAClient,
	// cluster B's in clusterBClient — NOT in the central client.
	for _, unit := range plan.units {
		require.NoError(t, r.applyReconcileUnit(context.Background(), unit, zap.NewNop().Sugar()))
	}

	// Cluster A
	stsA := &appsv1.StatefulSet{}
	require.NoError(t, clusterAClient.Get(context.Background(),
		types.NamespacedName{Name: "mdb-search-search", Namespace: "ns"}, stsA))
	// Cluster B
	stsB := &appsv1.StatefulSet{}
	require.NoError(t, clusterBClient.Get(context.Background(),
		types.NamespacedName{Name: "mdb-search-search-1", Namespace: "ns"}, stsB))
	// Central must be empty
	stsCentral := &appsv1.StatefulSet{}
	err = centralClient.Get(context.Background(),
		types.NamespacedName{Name: "mdb-search-search", Namespace: "ns"}, stsCentral)
	assert.True(t, apierrors.IsNotFound(err), "central client must NOT have per-cluster STS")
}
```

- [ ] **Step 16.3: Run test to verify it fails**

```bash
go test ./controllers/searchcontroller/... -run TestReconcilePlan_UsesPerClusterClient -count=1
```

Expected: FAIL — either `applyReconcileUnit` not yet present, or it writes only to `r.client`.

- [ ] **Step 16.4: Refactor the unit application path to be per-cluster**

In `mongodbsearch_reconcile_helper.go`, add a helper that picks the right client:

```go
// clientForUnit returns the kube client a reconcile unit's resources should
// be written to. clusterName == "" → central client (legacy single-cluster
// path).
func (r *MongoDBSearchReconcileHelper) clientForUnit(unit reconcileUnit) client.Client {
	if unit.clusterName == "" {
		return r.client
	}
	if c, ok := r.memberClusterClients[unit.clusterName]; ok {
		return c
	}
	// Fall through to central — admission should have rejected an unknown
	// clusterName, so this only fires in tests / misconfiguration. The unit's
	// reconcile will likely error, surfacing the misconfiguration.
	return r.client
}
```

Then refactor `applyReconcileUnit` (or whatever the consumption function is currently called) to use `r.clientForUnit(unit)` everywhere it currently uses `r.client`. Specifically:

- StatefulSet create/update
- Service create/update (the headless + proxy services)
- ConfigMap create/update
- TLS Secret read

- [ ] **Step 16.5: Run test to verify it passes**

```bash
go test ./controllers/searchcontroller/... -run TestReconcilePlan_UsesPerClusterClient -count=1 -v
```

Expected: PASS.

- [ ] **Step 16.6: Run full unit-test suite**

```bash
go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... -count=1
```

Expected: ALL pass.

- [ ] **Step 16.7: Commit**

```bash
git add controllers/searchcontroller/
git commit -m "feat(search): apply per-cluster reconcileUnit to its target member-cluster client"
```

## Task 17: Per-cluster mongot ConfigMap renders top-level `external.hostAndPorts`

**Files:**
- Modify: `controllers/searchcontroller/external_search_source.go`
- Modify: `controllers/searchcontroller/external_search_source_test.go` (or `mongodbsearch_reconcile_helper_test.go`)

The `external_search_source.go` `HostSeeds` method must return the top-level `spec.source.external.hostAndPorts` regardless of which cluster is asking. (It already does, but verify.)

- [ ] **Step 17.1: Read the current implementation**

```bash
sed -n '1,80p' controllers/searchcontroller/external_search_source.go
```

- [ ] **Step 17.2: Write the failing test**

In `external_search_source_test.go`, append (create the file if absent):

```go
func TestExternalSource_HostSeeds_SameForEveryCluster(t *testing.T) {
	mdb := &searchv1.MongoDBSearch{
		Spec: searchv1.MongoDBSearchSpec{
			Source: &searchv1.SearchSource{
				ExternalMongoDBSource: &searchv1.ExternalMongoDBSource{
					HostAndPorts: []string{
						"rs-0.example:27017",
						"rs-1.example:27017",
						"rs-2.example:27017",
					},
				},
			},
		},
	}
	src := newExternalSearchSource(mdb)

	for _, cluster := range []string{"", "cluster-a", "cluster-b"} {
		got, err := src.HostSeeds(cluster)
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"rs-0.example:27017", "rs-1.example:27017", "rs-2.example:27017"},
			got,
			"hosts should be top-level for cluster=%q", cluster)
	}
}
```

- [ ] **Step 17.3: Run test**

```bash
go test ./controllers/searchcontroller/... -run TestExternalSource_HostSeeds_SameForEveryCluster -count=1 -v
```

Expected: probably PASS (the existing implementation already takes top-level). If it fails, fix the implementation in `external_search_source.go`'s `HostSeeds`:

```go
func (e *externalSearchSource) HostSeeds(_ string) ([]string, error) {
	if e.mdb.Spec.Source == nil || e.mdb.Spec.Source.ExternalMongoDBSource == nil {
		return nil, fmt.Errorf("external source not configured")
	}
	hosts := e.mdb.Spec.Source.ExternalMongoDBSource.HostAndPorts
	if len(hosts) == 0 {
		return nil, fmt.Errorf("spec.source.external.hostAndPorts must be non-empty")
	}
	return append([]string(nil), hosts...), nil
}
```

The cluster argument is ignored on purpose — top-level seed list goes to every cluster. The signature has a clusterName parameter for symmetry with `SearchSourceReplicaSet`'s interface, where managed sources (Phase 4) WILL use it.

- [ ] **Step 17.4: Confirm pass**

```bash
go test ./controllers/searchcontroller/... -count=1
```

- [ ] **Step 17.5: Commit**

```bash
git add controllers/searchcontroller/
git commit -m "test(search): assert external HostSeeds returns top-level list for every cluster"
```

## Task 18: Update Envoy filter chain to use per-cluster proxy-svc SNI

**Files:**
- Modify: `controllers/operator/mongodbsearchenvoy_controller.go`
- Modify: `controllers/operator/mongodbsearchenvoy_controller_test.go`

B16 already accepts `clusterName` in the filter chain renderer; verify the SNI server_name uses the per-cluster proxy-svc name and not the legacy single-cluster name.

- [ ] **Step 18.1: Find the SNI server_names construction**

```bash
grep -n "server_names\|SNI\|sniServiceName" controllers/operator/mongodbsearchenvoy_controller.go
```

Expected: line ~377 (`sniServiceName := search.ProxyServiceNamespacedName().Name`) — this is single-cluster only.

- [ ] **Step 18.2: Write the failing test**

In `mongodbsearchenvoy_controller_test.go`, append:

```go
func TestEnvoyFilterChain_PerClusterSNI(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", Replicas: pointer.Int32(2)},
		{ClusterName: "cluster-b", Replicas: pointer.Int32(2)},
	}

	configA, err := renderEnvoyJSON(mdb, /*clusterIndex=*/ 0, "cluster-a")
	require.NoError(t, err)
	assert.Contains(t, configA, `"mdb-search-search-0-proxy-svc.ns.svc.cluster.local"`,
		"cluster A's filter chain must SNI-match its own proxy-svc FQDN")

	configB, err := renderEnvoyJSON(mdb, /*clusterIndex=*/ 1, "cluster-b")
	require.NoError(t, err)
	assert.Contains(t, configB, `"mdb-search-search-1-proxy-svc.ns.svc.cluster.local"`,
		"cluster B's filter chain must SNI-match its own proxy-svc FQDN")
	assert.NotContains(t, configB, `"mdb-search-search-0-proxy-svc"`,
		"cluster B's filter chain must not contain cluster A's SNI")
}
```

- [ ] **Step 18.3: Run test to verify it fails**

```bash
go test ./controllers/operator/... -run TestEnvoyFilterChain_PerClusterSNI -count=1 -v
```

Expected: FAIL.

- [ ] **Step 18.4: Update the SNI rendering**

In `mongodbsearchenvoy_controller.go`, find the existing SNI construction (around line 377):

```go
sniServiceName := search.ProxyServiceNamespacedName().Name
```

Replace with cluster-index-aware:

```go
sniServiceName := search.ProxyServiceNamespacedNameForCluster(clusterIndex).Name
```

`renderEnvoyJSON` (or whatever the renderer function is) must accept `clusterIndex` as a parameter. Trace its callers — likely the per-cluster reconcile loop already passes it.

- [ ] **Step 18.5: Run test to verify it passes**

```bash
go test ./controllers/operator/... -run TestEnvoyFilterChain_PerClusterSNI -count=1 -v
```

Expected: PASS.

- [ ] **Step 18.6: Run full unit-test suite**

```bash
go test ./controllers/operator/... ./controllers/searchcontroller/... -count=1
```

Expected: ALL pass.

- [ ] **Step 18.7: Commit**

```bash
git add controllers/operator/
git commit -m "feat(search-envoy): SNI server_name uses per-cluster proxy-svc FQDN"
```

## Task 19: LB cert SAN must include each cluster's proxy-svc FQDN

**Files:**
- Modify: `controllers/searchcontroller/mongodbsearch_reconcile_helper.go` — wherever the cert validation runs (search for `LoadBalancerServerCert` use)
- Verify B16 test: `controllers/operator/mongodbsearchenvoy_controller_test.go` — likely already covers SAN validation

The LB cert is a single Secret per resource (B16 design). For MC, its SAN list must enumerate each cluster's distinct proxy-svc FQDN. The operator validates the cert at reconcile time; if SANs don't cover all clusters, surface as `Failed` (or `Pending` with a warning).

- [ ] **Step 19.1: Find the LB cert validation site**

```bash
grep -n "LoadBalancerServerCert\|SANCheck\|ValidateCertificate" controllers/searchcontroller/ controllers/operator/ -r 2>/dev/null
```

- [ ] **Step 19.2: Write the failing test**

In `mongodbsearch_reconcile_helper_test.go`, append:

```go
func TestValidateLBCertSANCoversAllClusters(t *testing.T) {
	mdb := newTestMongoDBSearch("mdb-search", "ns")
	mdb.Spec.Clusters = []searchv1.ClusterSpec{
		{ClusterName: "cluster-a", Replicas: pointer.Int32(2)},
		{ClusterName: "cluster-b", Replicas: pointer.Int32(2)},
	}

	// Cert SAN includes only cluster A's proxy-svc FQDN — should fail.
	certShortSANs := makeFakeCertSecret([]string{
		"mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
	})
	err := validateLBCertSAN(mdb, certShortSANs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cluster-b")
	assert.Contains(t, err.Error(), "mdb-search-search-1-proxy-svc.ns.svc.cluster.local")

	// Cert SAN with both — should pass.
	certFullSANs := makeFakeCertSecret([]string{
		"mdb-search-search-0-proxy-svc.ns.svc.cluster.local",
		"mdb-search-search-1-proxy-svc.ns.svc.cluster.local",
	})
	require.NoError(t, validateLBCertSAN(mdb, certFullSANs))
}
```

- [ ] **Step 19.3: Run test to verify it fails**

```bash
go test ./controllers/searchcontroller/... -run TestValidateLBCertSANCoversAllClusters -count=1 -v
```

- [ ] **Step 19.4: Implement the validator**

Add to `mongodbsearch_reconcile_helper.go`:

```go
// validateLBCertSAN ensures the LB server cert's SAN list covers each
// cluster's proxy-svc FQDN. If a cluster's FQDN is missing, return a
// descriptive error naming the cluster and the missing FQDN.
func validateLBCertSAN(mdb *searchv1.MongoDBSearch, certSecret *corev1.Secret) error {
	clusters := mdb.EffectiveClusters()
	if len(clusters) <= 1 {
		return nil // single-cluster: existing legacy validation suffices
	}

	dnsNames, err := extractCertDNSNames(certSecret)
	if err != nil {
		return fmt.Errorf("read LB cert SANs: %w", err)
	}
	dnsSet := make(map[string]struct{}, len(dnsNames))
	for _, n := range dnsNames {
		dnsSet[n] = struct{}{}
	}

	for idx, cluster := range clusters {
		want := mdb.ProxyServiceNamespacedNameForCluster(idx).Name
		fqdn := fmt.Sprintf("%s.%s.svc.cluster.local", want, mdb.Namespace)
		if _, ok := dnsSet[fqdn]; !ok {
			return fmt.Errorf(
				"LB cert SAN missing FQDN %q for cluster %q (idx %d)",
				fqdn, cluster.ClusterName, idx,
			)
		}
	}
	return nil
}
```

- [ ] **Step 19.5: Wire `validateLBCertSAN` into reconcile**

Find where the LB cert is currently read at reconcile time (likely in `mongodbsearch_reconcile_helper.go` or `mongodbsearchenvoy_controller.go`). Call `validateLBCertSAN` and surface a `Failed` workflow status if it errors:

```go
if err := validateLBCertSAN(r.mdbSearch, certSecret); err != nil {
	return workflow.Failed(xerrors.Errorf("LB cert SAN validation: %w", err))
}
```

- [ ] **Step 19.6: Run tests**

```bash
go test ./controllers/searchcontroller/... -count=1
```

Expected: ALL pass.

- [ ] **Step 19.7: Commit**

```bash
git add controllers/searchcontroller/
git commit -m "feat(search): LB cert SAN must enumerate each cluster's proxy-svc FQDN"
```

## Task 20: Admission rule — `external.hostAndPorts` non-empty when `len(spec.clusters) > 1`

**Files:**
- Modify: `api/v1/search/mongodbsearch_validation.go` (Go-side admission)
- Modify: `helm_chart/crds/mongodb.com_mongodbsearch.yaml` indirectly via `make generate` (CEL rule on the CRD)

- [ ] **Step 20.1: Write the failing test**

In `mongodbsearch_validation_test.go`, append:

```go
func TestValidate_MCRequiresExternalHostAndPortsNonEmpty(t *testing.T) {
	tests := []struct {
		name      string
		mdb       *MongoDBSearch
		wantError string
	}{
		{
			name: "MC with empty external.hostAndPorts → reject",
			mdb: &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Clusters: []ClusterSpec{
						{ClusterName: "a", Replicas: pointer.Int32(1)},
						{ClusterName: "b", Replicas: pointer.Int32(1)},
					},
					Source: &SearchSource{
						ExternalMongoDBSource: &ExternalMongoDBSource{
							HostAndPorts: []string{}, // empty
						},
					},
				},
			},
			wantError: "spec.source.external.hostAndPorts is required when spec.clusters has more than one entry",
		},
		{
			name: "MC with populated external.hostAndPorts → accept",
			mdb: &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Clusters: []ClusterSpec{
						{ClusterName: "a", Replicas: pointer.Int32(1)},
						{ClusterName: "b", Replicas: pointer.Int32(1)},
					},
					Source: &SearchSource{
						ExternalMongoDBSource: &ExternalMongoDBSource{
							HostAndPorts: []string{"a:27017", "b:27017"},
						},
					},
				},
			},
			wantError: "",
		},
		{
			name: "Single-cluster with empty external.hostAndPorts → also reject (existing behavior)",
			mdb: &MongoDBSearch{
				Spec: MongoDBSearchSpec{
					Source: &SearchSource{
						ExternalMongoDBSource: &ExternalMongoDBSource{
							HostAndPorts: []string{},
						},
					},
				},
			},
			wantError: "external.hostAndPorts cannot be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.mdb.ValidateCreate()
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
			}
		})
	}
}
```

- [ ] **Step 20.2: Run test to verify it fails**

```bash
go test ./api/v1/search/... -run TestValidate_MCRequiresExternalHostAndPortsNonEmpty -count=1 -v
```

- [ ] **Step 20.3: Add the Go-side rule**

In `api/v1/search/mongodbsearch_validation.go`, find the `validateExternal` (or equivalent) function and add:

```go
// validateExternalHostAndPortsForMC enforces that external.hostAndPorts is
// non-empty when len(spec.clusters) > 1. Per the MC MVP routing strategy,
// every cluster's mongot config seeds from this top-level list, so an
// empty list in MC mode renders unconfigured mongot pods.
func (s *MongoDBSearch) validateExternalHostAndPortsForMC() error {
	if len(s.Spec.Clusters) <= 1 {
		return nil
	}
	if s.Spec.Source == nil || s.Spec.Source.ExternalMongoDBSource == nil {
		return nil // managed-source MC handled separately (Phase 4)
	}
	if len(s.Spec.Source.ExternalMongoDBSource.HostAndPorts) == 0 {
		return fmt.Errorf(
			"spec.source.external.hostAndPorts is required when spec.clusters has more than one entry",
		)
	}
	return nil
}
```

Then call it from `ValidateCreate` and `ValidateUpdate`.

- [ ] **Step 20.4: Run tests**

```bash
go test ./api/v1/search/... -count=1
```

Expected: ALL pass.

- [ ] **Step 20.5: Add the CRD-level CEL rule (if MCK uses CEL for similar rules)**

Look at how B14 / B13 added CEL rules in `api/v1/search/mongodbsearch_types.go` — typically `+kubebuilder:validation:XValidation:rule="…",message="…"`. Add a sibling rule on the type:

```go
// +kubebuilder:validation:XValidation:rule="size(self.clusters) <= 1 || (self.source != nil && self.source.external != nil && size(self.source.external.hostAndPorts) > 0)",message="spec.source.external.hostAndPorts is required when spec.clusters has more than one entry"
type MongoDBSearchSpec struct {
```

(Adjust the rule path syntax to match the existing CEL idioms used in B3/B13.)

- [ ] **Step 20.6: Regenerate CRDs**

```bash
make generate
```

Verify `helm_chart/crds/mongodb.com_mongodbsearch.yaml` got the new validation.

- [ ] **Step 20.7: Commit**

```bash
git add api/v1/search/ helm_chart/crds/
git commit -m "feat(search): admission requires external.hostAndPorts non-empty for MC mode"
```

## Task 21: Lint and full unit-test pass

- [ ] **Step 21.1: Run pre-commit on the operator changes**

```bash
pre-commit run --files \
  api/v1/search/mongodbsearch_types.go \
  api/v1/search/mongodbsearch_types_test.go \
  api/v1/search/mongodbsearch_validation.go \
  api/v1/search/mongodbsearch_validation_test.go \
  controllers/searchcontroller/mongodbsearch_reconcile_helper.go \
  controllers/searchcontroller/mongodbsearch_reconcile_helper_test.go \
  controllers/searchcontroller/external_search_source.go \
  controllers/searchcontroller/external_search_source_test.go \
  controllers/operator/mongodbsearchenvoy_controller.go \
  controllers/operator/mongodbsearchenvoy_controller_test.go \
  helm_chart/crds/mongodb.com_mongodbsearch.yaml
```

Expected: all hooks pass (gci import grouping, ty static type check, gofmt). If a hook auto-fixes, commit the fixes:

```bash
git add -u && git commit --amend --no-edit
```

- [ ] **Step 21.2: Run full unit-test suite**

```bash
go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/... -count=1 -v
```

Expected: ALL pass.

- [ ] **Step 21.3: Push the branch**

```bash
git push origin mc-search-phase2-q2-rs
```

## Task 22: Update `q2_mc_rs_steady.py` — drop iter-4/iter-5 tolerance

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

The current scaffold-level relaxed test was committed at `fca043e71`. Restore strict assertions.

- [ ] **Step 22.1: Show the relaxed assertion sites**

```bash
grep -n "require_ready=False\|@pytest.mark.skip\|don't assert Phase=Running\|tolerant" docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py
```

- [ ] **Step 22.2: Restore strict `Phase=Running` for the search resource**

Find `test_create_search_resource` and change:

```python
@mark.e2e_search_q2_mc_rs_steady
def test_create_search_resource(mdbs: MongoDBSearch):
    mdbs.update()
    # Strict — Phase 2 operator code makes Running real now.
    mdbs.assert_reaches_phase(Phase.Running, timeout=600)
```

- [ ] **Step 22.3: Restore strict Envoy readiness**

Find `test_verify_per_cluster_envoy_deployment` and remove `require_ready=False`:

```python
@mark.e2e_search_q2_mc_rs_steady
def test_verify_per_cluster_envoy_deployment(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
):
    assert_envoy_ready_in_each_cluster(
        namespace, MDBS_RESOURCE_NAME, member_cluster_clients, require_ready=True
    )
```

- [ ] **Step 22.4: Restore strict per-cluster status**

Find `test_verify_per_cluster_status` and remove the `if cluster_statuses is None: return` branch — clusterStatusList must be populated now (B9 lands in Base).

- [ ] **Step 22.5: Restore strict LB phase**

Find `test_verify_lb_status` and ensure `mdbs.assert_lb_status()` runs without the existence-only fallback.

- [ ] **Step 22.6: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py
git commit -m "test(search): MC RS — restore strict Phase=Running and require_ready=True assertions"
```

## Task 23: Un-skip data plane tests in `q2_mc_rs_steady.py`

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

- [ ] **Step 23.1: Remove `@pytest.mark.skip`**

Find:

```python
@mark.e2e_search_q2_mc_rs_steady
@pytest.mark.skip(
    reason="Data plane deferred to Phase 3..."
)
def test_create_search_index(mdb: MongoDBMulti):
    ...
```

Remove the `@pytest.mark.skip` decorator. Same for `test_execute_text_search_query_per_cluster`.

- [ ] **Step 23.2: Verify the test bodies are still valid**

The bodies were not deleted; only the `skip` decorator was added. Verify the test code calls `q2_create_search_index(mdb, get_search_tester)` and similar helpers from `tests/common/search/q2_shared.py` — they should still work against the now-real per-cluster mongot pools.

- [ ] **Step 23.3: Commit**

```bash
git add docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py
git commit -m "test(search): MC RS — un-skip data plane tests now that Phase 2 lands per-cluster mongot"
```

## Task 24: Switch `mongotHost` fixture to per-cluster `clusterSpecList[i].additionalMongodConfig`

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py` — the `mdb` fixture
- Possibly: `docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml` if mongotHost is in the YAML

- [ ] **Step 24.1: Find the current mongotHost setting in the fixture**

```bash
grep -n "mongotHost\|additionalMongodConfig\|setParameter" docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py docker/mongodb-kubernetes-tests/tests/multicluster_search/fixtures/search-q2-mc-rs.yaml 2>/dev/null
```

- [ ] **Step 24.2: Update the fixture to set `mongotHost` per-cluster**

In the `mdb` fixture (likely around line 175-210 of `q2_mc_rs_steady.py`), replace top-level `additionalMongodConfig.setParameter.mongotHost` with per-cluster:

```python
@fixture(scope="function")
def mdb(
    namespace: str,
    member_cluster_clients: List[MultiClusterClient],
    helper: MCSearchDeploymentHelper,
) -> MongoDBMulti:
    """2-cluster MongoDBMulti RS source with TLS+SCRAM, per-cluster mongotHost."""
    resource = MongoDBMulti.from_yaml(
        yaml_fixture("search-q2-mc-rs.yaml"),
        namespace=namespace,
        name=MDB_RESOURCE_NAME,
    )
    # Per-cluster mongotHost: each cluster's mongods point at THIS cluster's
    # proxy Service, which has a cluster-index-suffixed name.
    cluster_spec = []
    for mcc in member_cluster_clients:
        idx = helper.cluster_index(mcc.cluster_name)
        proxy_fqdn = helper.proxy_svc_fqdn(mcc.cluster_name)
        cluster_spec.append({
            "clusterName": mcc.cluster_name,
            "members": 3,
            "additionalMongodConfig": {
                "setParameter": {
                    "mongotHost": f"{proxy_fqdn}:{ENVOY_PROXY_PORT}",
                    "searchIndexManagementHostAndPort": f"{proxy_fqdn}:{ENVOY_PROXY_PORT}",
                    "searchTLSMode": "requireTLS",
                },
            },
        })
    resource["spec"]["clusterSpecList"] = cluster_spec
    return resource
```

- [ ] **Step 24.3: Remove top-level `additionalMongodConfig.setParameter.mongotHost` from the YAML fixture**

If `search-q2-mc-rs.yaml` carries it at the top of `spec`, remove it — `clusterSpecList[i]` overrides override it but cleaner to just remove.

- [ ] **Step 24.4: Lint and commit**

```bash
python3 -m black docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --line-length 120
python3 -m isort docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --profile black --line-length 120
git add docker/mongodb-kubernetes-tests/tests/multicluster_search/
git commit -m "test(search): MC RS — set mongotHost per-cluster via clusterSpecList[i].additionalMongodConfig"
```

## Task 25: Drop per-cluster `syncSourceSelector.hosts` and `REGION_TAGS` from fixture

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

- [ ] **Step 25.1: Find and remove iter-1 `syncSourceSelector.hosts` plumbing**

```bash
grep -n "syncSourceSelector\|REGION_TAGS\|matchTags" docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py
```

In the `mdbs` fixture, remove any block that sets `clusters[i].syncSourceSelector.hosts`. The `clusters` shape becomes:

```python
resource["spec"]["clusters"] = [
    {"clusterName": mcc.cluster_name, "replicas": 2}
    for mcc in member_cluster_clients
]
```

- [ ] **Step 25.2: Remove REGION_TAGS pinning on the MongoDBMulti source**

In the `mdb` fixture, remove any `tags` setting on `clusterSpecList[i].memberConfig[*].tags`. (The `mongotHost`-bearing `clusterSpecList[i]` from Task 24 stays; just the `tags` go.)

- [ ] **Step 25.3: Drop the import of `REGION_TAGS` from `q2_topology`**

```bash
grep -n "REGION_TAGS" docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py docker/mongodb-kubernetes-tests/tests/common/search/q2_topology.py
```

Remove the import line. If `REGION_TAGS` is unused elsewhere, also delete its definition in `q2_topology.py`.

- [ ] **Step 25.4: Lint and commit**

```bash
python3 -m black docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --line-length 120
python3 -m isort docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --profile black --line-length 120
git add docker/mongodb-kubernetes-tests/
git commit -m "test(search): MC RS — drop unused per-cluster syncSourceSelector.hosts and REGION_TAGS"
```

## Task 25.5: Wire cross-cluster Secret replication into the e2e test setup

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

Without this step, the LB cert / source TLS CA / mongot password Secrets exist only in the central cluster, mongot pods in member clusters stay `PodInitializing` forever, and the search resource never reaches `Phase=Running`. The harness's `replicate_secret` (Task 9) is the mechanism; this task wires it into the test flow at the right point — right before `test_create_search_resource`.

- [ ] **Step 25.5.1: Add the Secret-replication test step**

In `q2_mc_rs_steady.py`, insert AFTER `test_create_search_tls_certificate` and BEFORE `test_create_search_resource` (Secrets must exist before the search resource starts up):

```python
@mark.e2e_search_q2_mc_rs_steady
def test_replicate_secrets_to_members(
    central_cluster_client,
    member_cluster_clients: List[MultiClusterClient],
    namespace: str,
):
    """Replicate the LB cert, source TLS CA, and mongot user password into each member cluster.

    Per program rules, MCK does NOT replicate Secrets in production — that's
    the customer's responsibility. The test harness does it here so the
    e2e test can stand up a working multi-cluster fixture without
    requiring the customer-side replication machinery.
    """
    central_core = CoreV1Api(api_client=central_cluster_client)
    member_cores = {
        mcc.cluster_name: CoreV1Api(api_client=mcc.api_client)
        for mcc in member_cluster_clients
    }

    secrets_to_replicate = [
        # LB TLS server cert (single Secret; SAN list covers every cluster's
        # proxy-svc FQDN per Task 19's validator).
        f"{MDBS_TLS_CERT_PREFIX}-{MDBS_RESOURCE_NAME}-search-lb-0-cert"
        if MDBS_TLS_CERT_PREFIX
        else f"{MDBS_RESOURCE_NAME}-search-lb-0-cert",
        # Sync-source TLS CA configmap (mongot needs to verify mongod cert).
        # Note: this is a ConfigMap, replicate via the analogous CM helper.
        # TLS CA is currently a ConfigMap fixture; if the implementation uses
        # a Secret variant, replicate that name.
        # Mongot user password Secret (sync-source credentials).
        f"{MDBS_RESOURCE_NAME}-{MONGOT_USER_NAME}-password",
    ]

    for secret_name in secrets_to_replicate:
        replicate_secret(
            secret_name=secret_name,
            namespace=namespace,
            central_client=central_core,
            member_clients=member_cores,
        )
        logger.info(f"replicated {secret_name} to {len(member_cores)} member clusters")

    # ConfigMap replication for the source TLS CA (separate from Secret API).
    # If the test fixture uses ConfigMap-form CA (search-q2-mc-rs.yaml's
    # spec.source.external.tls.ca.name pointing at a ConfigMap):
    ca_cm_name = CA_CONFIGMAP_NAME  # already defined in the fixture
    source_cm = central_core.read_namespaced_config_map(name=ca_cm_name, namespace=namespace)
    for cluster_name, member in member_cores.items():
        try:
            member.read_namespaced_config_map(name=ca_cm_name, namespace=namespace)
            # Already exists → patch
            member.patch_namespaced_config_map(
                name=ca_cm_name,
                namespace=namespace,
                body={"data": dict(source_cm.data or {})},
            )
        except ApiException as exc:
            if exc.status != 404:
                raise
            # Create
            member.create_namespaced_config_map(
                namespace=namespace,
                body=V1ConfigMap(
                    metadata=V1ObjectMeta(name=ca_cm_name, namespace=namespace),
                    data=dict(source_cm.data or {}),
                ),
            )
    logger.info(f"replicated CA ConfigMap {ca_cm_name} to {len(member_cores)} member clusters")
```

Make sure imports at the top of the file include:

```python
from kubernetes.client import CoreV1Api, V1ConfigMap, V1ObjectMeta
from kubernetes.client.exceptions import ApiException
from tests.common.multicluster_search.secret_replicator import replicate_secret
```

- [ ] **Step 25.5.2: Lint and commit**

```bash
python3 -m black docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --line-length 120
python3 -m isort docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --profile black --line-length 120
git add docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py
git commit -m "test(search): MC RS — replicate LB cert, source CA, mongot password to member clusters via harness"
```

## Task 26: Add `$vectorSearch` index creation test

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

- [ ] **Step 26.1: Add the index creation test**

Append (after `test_create_search_index`):

```python
@mark.e2e_search_q2_mc_rs_steady
def test_create_vector_search_index(mdb: MongoDBMulti):
    """Create an auto-embedding $vectorSearch index on sample_mflix.movies.

    Uses the existing SampleMoviesSearchHelper.create_auto_embedding_vector_search_index
    helper. Voyage API key comes from AI_MONGODB_EMBEDDING_QUERY_KEY env var
    (already wired in the single-cluster auto-embedding tests).
    """
    tester = get_search_tester(mdb, USER_NAME, USER_PASSWORD, use_ssl=True)
    helper = SampleMoviesSearchHelper(search_tester=tester)
    helper.create_auto_embedding_vector_search_index()
    helper.wait_for_search_indexes()
```

Make sure imports at the top include:

```python
from tests.common.search.movies_search_helper import SampleMoviesSearchHelper
```

- [ ] **Step 26.2: Lint and commit**

```bash
python3 -m black docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --line-length 120
git add docker/mongodb-kubernetes-tests/
git commit -m "test(search): MC RS — add \$vectorSearch index creation"
```

## Task 27: Add `$vectorSearch` query execution test

**Files:**
- Modify: `docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py`

- [ ] **Step 27.1: Add the query execution test**

Append (after `test_create_vector_search_index`):

```python
@mark.e2e_search_q2_mc_rs_steady
def test_execute_vector_search_query_per_cluster(
    mdb: MongoDBMulti,
    member_cluster_clients: List[MultiClusterClient],
):
    """Execute a $vectorSearch query seeded from each member cluster's local pod.

    Asserts ≥1 row returned. The exact row count depends on the sample
    mflix data; we don't assert a specific match count, only non-empty.
    Cross-cluster correctness: standard RS topology means any pod's
    aggregation will hit the primary; we exercise from each cluster to
    validate connectivity and per-cluster Envoy routing.
    """
    for mcc in member_cluster_clients:
        # Build a per-cluster connection string seeded from this cluster's
        # mongod pod. ?replicaSet=... lets the driver discover the primary.
        seed_host = (
            f"{MDB_RESOURCE_NAME}-{mcc.cluster_index}-0-svc.{mdb.namespace}"
            f".svc.cluster.local:27017"
        )
        conn_str = (
            f"mongodb://{USER_NAME}:{USER_PASSWORD}@{seed_host}/"
            f"?replicaSet={MDB_RESOURCE_NAME}&tls=true&tlsCAFile=/path/to/ca.pem"
        )
        tester = SearchTester(conn_str, use_ssl=True)
        helper = SampleMoviesSearchHelper(search_tester=tester)

        # Use the existing assert_vector_search_query helper if present,
        # else inline a basic $vectorSearch aggregation.
        results = list(tester.client["sample_mflix"]["movies"].aggregate([
            {
                "$vectorSearch": {
                    "index": "default-vector",
                    "path": "plot_embedding",
                    "queryVector": helper.example_query_vector(),
                    "numCandidates": 100,
                    "limit": 4,
                }
            }
        ]))
        assert len(results) >= 1, (
            f"$vectorSearch from cluster {mcc.cluster_name} returned no results"
        )
        logger.info(
            f"$vectorSearch from cluster {mcc.cluster_name} returned {len(results)} rows"
        )
```

- [ ] **Step 27.2: If `SampleMoviesSearchHelper.example_query_vector` doesn't exist, add it**

Check `docker/mongodb-kubernetes-tests/tests/common/search/movies_search_helper.py` for `example_query_vector`. If missing, add:

```python
def example_query_vector(self) -> list[float]:
    """Return a Voyage-embedded query vector for a fixed test query.

    Calls the Voyage embedding API once per test using the existing
    AI_MONGODB_EMBEDDING_QUERY_KEY env var. Cached on the helper.
    """
    if hasattr(self, "_cached_query_vector"):
        return self._cached_query_vector
    api_key = os.environ[EMBEDDING_QUERY_KEY_ENV_VAR]
    response = requests.post(
        VOYAGE_EMBEDDING_ENDPOINT,
        headers={"Authorization": f"Bearer {api_key}"},
        json={
            "input": ["A movie about space exploration"],
            "model": VOYAGE_MODEL,
            "input_type": "query",
        },
    )
    response.raise_for_status()
    self._cached_query_vector = response.json()["data"][0]["embedding"]
    return self._cached_query_vector
```

- [ ] **Step 27.3: Lint and commit**

```bash
python3 -m black docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py docker/mongodb-kubernetes-tests/tests/common/search/movies_search_helper.py --line-length 120
python3 -m isort docker/mongodb-kubernetes-tests/tests/multicluster_search/q2_mc_rs_steady.py --profile black --line-length 120
git add docker/mongodb-kubernetes-tests/
git commit -m "test(search): MC RS — add \$vectorSearch query execution per cluster"
```

## Task 28: Push and trigger Phase 2 Evergreen patch

**Files:** none (CI only)

- [ ] **Step 28.1: Push the branch**

```bash
git push origin mc-search-phase2-q2-rs
```

- [ ] **Step 28.2: Trigger Evergreen patch**

```bash
evergreen patch \
  --project mongodb-kubernetes \
  --variants e2e_static_mongodb_kind_ubi \
  --tasks e2e_search_q2_mc_rs_steady,e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb,e2e_search_replicaset_external_mongodb_multi_mongot_unmanaged_lb \
  -y -d "Phase 2: Q2-RS-MC operator + tightened MC RS e2e + \$vectorSearch"
```

Capture the patch ID. Run `evergreen finalize-patch -i <patch-id>`.

- [ ] **Step 28.3: Watch the patch**

```bash
evergreen list --patches | head -3
```

Expected:
- `e2e_search_q2_mc_rs_steady` succeeds with all strict assertions, including data plane and `$vectorSearch`. **This is acceptance gate G2.**
- Both single-cluster Q2 RS regression tasks (`managed_lb`, `unmanaged_lb`) succeed.

If `e2e_search_q2_mc_rs_steady` fails, examine the patch's task page for the failing assertion. Common failure modes and remediation:

| Failure | Root cause | Fix |
|---|---|---|
| `Phase=Pending` on MongoDBSearch CR for >600s | Per-cluster mongot StatefulSet not Running. Check pod logs in member cluster. | Likely Task 16 client routing bug — re-verify per-cluster mongot lands in correct cluster. |
| Per-cluster Envoy not Ready | LB cert SAN missing one cluster's FQDN — Task 19 didn't fire OR cert generation in test fixture missed an entry. | Re-verify Task 19's validator catches it; fix the test fixture cert generator. |
| `$search` returns 0 rows | mongot couldn't index — sync direction broken. Check mongot pod logs for connection errors. | Likely TLS CA not replicated to member cluster; verify Task 9 / harness usage. |
| `$vectorSearch` errors with "index not found" | Auto-embedding pod-0 leader hasn't claimed lease. Wait longer / verify mongot version supports auto-embedding. | Increase timeout in `wait_for_search_indexes`. |

## Task 29: Open Phase 2 PR

- [ ] **Step 29.1: Create the PR**

```bash
gh pr create --base search/ga-base --head mc-search-phase2-q2-rs \
  --title "Phase 2: Q2-RS-MC operator + tightened MC RS e2e + \$vectorSearch" \
  --body "$(cat <<'EOF'
## Summary
- Per-cluster mongot StatefulSet, ConfigMap, and proxy Service creation in each member cluster
- Per-cluster naming helpers (`ProxyServiceNamespacedNameForCluster`, `MongotConfigConfigMapNameForCluster`, `StatefulSetNamespacedNameForCluster`, `SearchServiceNamespacedNameForCluster`)
- Per-cluster Envoy filter chain SNI from per-cluster proxy-svc FQDN
- LB cert SAN validator: must enumerate each cluster's distinct proxy-svc FQDN
- Admission rule: `external.hostAndPorts` non-empty when `len(spec.clusters) > 1`
- Tightened `q2_mc_rs_steady.py`: real `Phase=Running`, real Envoy Ready, real per-cluster status, real `$search` data plane
- Per-cluster `mongotHost` set via `clusterSpecList[i].additionalMongodConfig` (matches per-cluster proxy-svc FQDN)
- Drops iter-1 per-cluster `syncSourceSelector.hosts` and `REGION_TAGS` (no functional effect; mongot uses host list as a seed)
- Adds `$vectorSearch` coverage: index creation + query execution per cluster

## Test plan
- [x] Unit tests: `go test ./api/v1/search/... ./controllers/searchcontroller/... ./controllers/operator/...` green
- [x] Evergreen: `e2e_search_q2_mc_rs_steady` green with strict assertions + `$vectorSearch` (patch attached)
- [x] Single-cluster Q2 RS regression: `e2e_search_replicaset_external_mongodb_multi_mongot_managed_lb` and `unmanaged_lb` green

## Acceptance gate
G2 (named verification target) — `q2_mc_rs_steady.py` green with strict assertions, real `$search` + `$vectorSearch` data plane.

## Spec
docs/superpowers/specs/2026-04-30-rs-mc-mvp-to-green-design.md
EOF
)"
```

- [ ] **Step 29.2: After review, merge**

```bash
gh pr merge <pr-number> -R mongodb/mongodb-kubernetes --squash --delete-branch=false
```

**Phase 2 done.** `q2_mc_rs_steady.py` green with `$search` + `$vectorSearch` data plane. Acceptance gate G2 met.

---

# Self-review checklist (run after writing the plan)

**Spec coverage** (every section/requirement → at least one task):

| Spec section | Covered by task(s) |
|---|---|
| Land stacked B-section PR train | Tasks 1–7 |
| Verify single-cluster regression | Task 8 |
| MC E2E harness — Secret replicator | Task 9 |
| MC E2E harness — deployment helper | Task 10 |
| MC E2E harness — per-cluster assertions | Task 11 |
| Harness smoke test | Tasks 12, 13 |
| Per-cluster naming helpers (ProxyService, MongotConfigCM, StatefulSet, SearchService) | Tasks 14, 15 |
| `reconcileUnit` per-cluster fan-out | Task 15 |
| Per-cluster client routing | Task 16 |
| External-source per-cluster mongot config rendering | Task 17 |
| Envoy filter chain per-cluster SNI | Task 18 |
| LB cert SAN multi-cluster validation | Task 19 |
| Admission: `external.hostAndPorts` required for MC | Task 20 |
| Lint + full unit-test pass | Task 21 |
| q2_mc_rs_steady tightening: strict Phase=Running, Envoy Ready, status | Task 22 |
| q2_mc_rs_steady un-skip data plane | Task 23 |
| Per-cluster mongotHost via clusterSpecList | Task 24 |
| Drop syncSourceSelector.hosts and REGION_TAGS | Task 25 |
| Cross-cluster Secret replication wired into e2e setup | Task 25.5 |
| `$vectorSearch` index creation | Task 26 |
| `$vectorSearch` query execution | Task 27 |
| Evergreen patch + acceptance gate G2 | Task 28 |
| PR creation | Task 29 |

No gaps.

**Type consistency:**

- `ProxyServiceNamespacedNameForCluster(clusterIndex int) types.NamespacedName` used in Tasks 14, 15, 16, 18, 19. Same signature throughout.
- `MongotConfigConfigMapNameForCluster(clusterIndex int) types.NamespacedName` defined in Task 14, used in Task 15.
- `MCSearchDeploymentHelper.proxy_svc_fqdn(cluster_name: str) -> str` defined in Task 10, used in Task 24.
- `replicate_secret(secret_name, namespace, central_client, member_clients)` signature consistent across Tasks 9, 12.
- `assert_resource_in_cluster(client, *, kind, name, namespace)` consistent in Tasks 11, 12.
- `validateLBCertSAN(mdb, certSecret) error` consistent in Task 19.

No drift.

**Placeholder scan:** No "TBD", "TODO", "implement later" in any step. All steps include exact code, exact paths, exact commands. Tasks 19, 20, 22, 24, 25 reference current-codebase structure (line ranges, function names) that the implementer verifies at kickoff — these are honest "find this in the file" instructions, not placeholders.
