# ST1: Agent and MongoDB container split

**Status:** In progress. Test fixes for patch #8863 are on the MCK branch.
Live acceptance remains open.

**Audience:** MongoDB engineers and coding agents working on ST1.

**Snapshot:** 2026-11-05. CI status can change after this date.

## Coordination

This file is the single shared plan for ST1. Both pull requests link to it.
Read it before you change either branch.

| Repository | Pull request | Branch | Base |
|---|---|---|---|
| `10gen/mms-automation` (Agent) | [#14286](https://github.com/10gen/mms-automation/pull/14286) | `nammn-agent-process-maintainer` | `ops-manager-main` |
| `mongodb/mongodb-kubernetes` (MCK) | [#1827](https://github.com/mongodb/mongodb-kubernetes/pull/1827) | `nammn-st1-proxy-contract` | `KUBE-530/mongodb-proxy` |

Rules for engineers and agents:

1. Before you push to either branch, comment on its pull request. Push only
   after the owner acknowledges the comment. The owner is Nam Nguyen.
2. Do not force-push or rebase either branch. Use merge commits for base
   updates.
3. Add `[skip-ci]` to feature-branch commits. The owner starts Evergreen
   patches explicitly.
4. The Agent and MCK share two contracts: the proxy localhost API and the
   downstream expansion `mdb_custom_agent_url`. A change to one side needs a
   matching change on the other side, linked from both pull requests.
5. Update this file in the same commit as a scope or status change.

## Objective

Run the Automation Agent and MongoDB in separate containers for supported
Enterprise static workloads. The Agent keeps lifecycle decisions. A proxy
in the MongoDB container launches and observes the database process.

The Agent uses the proxy's localhost API for database lifecycle operations.
The proxy owns one `mongod` or `mongos` child and enforces the execution role.
The direct-process path remains available for Agent VM deployments.

MCK activates this path for `static-ga`. The explicit legacy `static` and
`non-static` paths keep their behavior. Missing prerequisites or an
incompatible Agent produce an error and do not replace healthy pods.

## Scope

The immediate target is amd64 Enterprise replica sets, sharded clusters,
multi-cluster deployments, and Ops Manager-managed databases.

AppDB e2e acceptance is deferred. The operator AppDB code and its unit tests
stay on the branch. The e2e tests do not assert static-ga AppDB behavior.

Supported Ops Manager versions are 8 and 9. Test fixtures use MongoDB
versions inside their lifecycle. Ops Manager 6 and 7 are outside ST1 support,
not removed from legacy support.

Upgrade tests keep their released-Agent starting phase. Non-static and Agent
VM behavior remain regression requirements.

Community proxy support, Magic Restore, MongoDB per-process CPU metric
restoration, signed staging, public releases, and non-amd64 acceptance are
deferred. An omitted test is not a passing test.

## Current state

| Area | State |
|---|---|
| Agent | PR #14286 includes the latest checked `ops-manager-main`. Focused Go tests pass on macOS. Linux-only and MongoDB-backed suites run only in Evergreen. |
| MCK | PR #1827 contains its current base `KUBE-530/mongodb-proxy` (`f88ff72a`), the `master` sync, and the test fixes below. |
| Agent artifact | [Build #8862](https://spruce.corp.mongodb.com/version/6aec42f51495e00007375e04) supplied the Agent for patch #8863. It predates the latest Agent base merge. |
| MCK CI | [Patch #8863](https://spruce.corp.mongodb.com/version/6aec44009953d000084bfbea) selected 739 e2e task/variant pairs with alias `static_ga_no_om67`. All 118 failed tasks have a classification. The patch predates the fixes below. |

## Fixes from patch #8863

All fixes are in MCK test code or CI scripts. No operator or Agent change.

| Failure group | Fix | Status |
|---|---|---|
| Multi-cluster owner label | Select `mongodbmulticluster=<namespace>-<name>`. Keep the bare name for `MongoDB`. | Done, unit-tested |
| Shard override adds a cluster | Apply the operator's override and inheritance rules. | Done, unit-tested |
| Pod selection by name prefix | Select the StatefulSet's own pods by selector and owner UID. | Done, unit-tested |
| Readiness transition | Poll the complete readiness condition within the existing deadline. | Done, unit-tested |
| Zero replicas | Assert that no owned pod runs. | Done, unit-tested |
| Vault TLS container count | `static`=4, `static-ga`=3, `non-static`=2. | Done, unit-tested |
| Migration operator lookup | Resolve the operator from the resource namespace. | Done, unit-tested |
| LDAP TLS convergence | Match process names `k8s/<namespace>/<pod>`. | Done |
| Upgrade test env vars | Merge operator env vars. Do not replace them. | Done, unit-tested |
| AppDB e2e assertions | Removed, because AppDB e2e acceptance is deferred. | Done |
| MongoDB 4.4.2 fixture | Use the suite's supported MongoDB version. | Done |
| Istio download | Bounded retry with checksum verification. | Done |

None of these fixes has a live e2e run yet.

## Unresolved

| Task | Evidence | Next step |
|---|---|---|
| `e2e_meko_mck_upgrade` | Cluster 3 never receives replica-set heartbeats. The primary logs `Connection reset by peer`. Both attempts fail. | Rerun with sidecar access logs to find the reset point. |
| `e2e_multi_cluster_backup_restore` (kind) | The oplog StatefulSet fails its startup probe. The probe output is truncated. An earlier attempt passed. | Capture the failing probe check before any change. |
| `e2e_sharded_cluster_shard_overrides` | Scale-down takes about 530 s against a 400 s limit. A slower candidate Agent is not excluded. | Compare with the released Agent before a timeout change. |
| `e2e_om_ops_manager_enable_local_mode_running_om` | Ops Manager rejects `9.0.2-ent`. A download returns HTTP 403. | Confirm the cause. |
| kind, jq, metrics-server downloads | HTTP 504 from upstream. | Retry. A mirror needs a separate decision. |

## Acceptance

1. Run the supported amd64 MCK static suite with the intended Agent and proxy.
   Run the applicable non-static and Community regression paths.
2. Run the Agent e2e suites and keep Agent VM behavior.
3. Verify the running Agent against the trusted producer SHA. Verify the
   proxy through its runtime behavior.
4. Verify replica sets, sharded clusters, and multi-cluster deployments,
   including upgrades, migrations, container restarts, and restores.
5. Check the minimum and maximum Kubernetes versions and the ImageVolume
   feature state. Record every approved skip.
6. Treat the known MCK Evergreen project-settings warning as a failed check.

## Open risks

- Production Agent/proxy compatibility detection is unresolved. The test-only
  compatibility input is not a production signal.
- A one-shot operation result can be lost across a proxy restart. Exactly-once
  recovery needs a durable witness or another verified contract.
- Non-amd64 static contexts exist, but current artifacts are amd64 only.
