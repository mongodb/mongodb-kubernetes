# KUBE-447 — Orphaned e2e tasks with a live pytest test: investigation

Investigation of the 8 orphaned e2e tasks that have a live pytest test but are disabled
(allowlisted in `scripts/evergreen/validate_evergreen_config.py` under the KUBE-447 group)
because they fail in CI. Failures were reproduced in Evergreen version
`6aa2d8033bb9690007e2237f` (patch `c7d82853f`).

Each task was investigated independently: Evergreen failure logs, the test source, fixtures,
and git history. All 8 are "defined but not used by any variants" — defined in
`.evergreen-tasks.yml` but not wired into any build variant / task group, so they only run
when a patch schedules them explicitly, and they bit-rot.

---

## 1. `e2e_standalone_groups`

- **Root cause (orphaned, no fresh CI failure):** removed in `CLOUDP-130604` (Remove OM 4.4,
  #2509, Sept 2022) when the whole `e2e_mongodb_non_cloudqa_task_group` was deleted. It cannot
  run against Cloud Manager QA — `setup_env` calls `create_organization()` via the OM API,
  which Cloud Manager programmatic keys forbid (per the class docstring). Never re-homed to a
  self-hosted OM variant. Task def still at `.evergreen-tasks.yml:122`.
- **Runnable today:** code is healthy (helpers + `standalone.yaml` fixture exist); needs a
  self-hosted OM variant. Fixture pins dated `6.0.5-ent`.
- **Possible fixes:**
  - Re-enable on an OM-based group (e.g. `e2e_ops_manager_kind_only_task_group` on
    `e2e_om80_kind_ubi`) — placement is the only fix; restores unique org→group coverage.
  - Re-enable on cloudqa: NO (org creation forbidden there).
  - Remove test+task — only if orgId-configmap flow is deprecated (it isn't; it's the standard
    project path).

## 2. `e2e_replica_set_groups`

- **Root cause:** `setup_env` fails with `403 API_KEY_CANNOT_CREATE_ORG` POSTing to
  `cloud-qa.mongodb.com/api/public/v1.0/orgs`. Ran on the `e2e_mdb_kind_ubi_cloudqa` variant
  where the backend is Cloud Manager QA, which forbids org creation via API keys. Never reached
  operator code.
- **Classification:** test/environment mismatch — not a product regression. Docstring says
  "skipped for cloud manager" but the `@skip_if_cloud_manager()` decorator is missing
  (sibling `standalone_groups.py` has it).
- **Possible fixes:**
  - Add class-level `@skip_if_cloud_manager()` and re-enable on a real-OM kind variant —
    pytest evaluates skipif before `setup_class`, so the 403 disappears. (Preferred.)
  - Remove test + task + allowlist entry — only if the pagination scenario is obsolete.

## 3. `e2e_replica_set_ldap_agent_auth`

- **Root cause:** `Timeout (400) waiting for ldap-replica-set Phase.Running — StatefulSet not
  ready`. Automation agent `13.57.0.11242` never converges mongod `5.0.7-ent`
  (`CUSTOM_MDB_PREV_VERSION`): it loops on `setParameter diffs [LDAP_QUERY_USER_RUNTIME,
  LDAP_QUERY_PASSWORD_RUNTIME, LDAP_USER_TO_DN_MAPPING_RUNTIME] → RollingChangeArgs restart →
  RollingRestartArgsEq=false`. The RS never initializes. Task commented out since operator
  1.30.0 (~2022).
- **Classification:** orphaned test + stale version baseline; agent/EOL-server incompatibility.
- **Possible fixes:**
  - Remove test + task (recommended) — orphaned ~4 years, validates an EOL server; also drop
    the `validate_evergreen_config.py` exemption.
  - Re-enable after fix — only if LDAP agent-auth upgrade-from-prev coverage is wanted: bump the
    prev baseline to a supported version and prove convergence.
  - Product ticket — only if agent-13.57-vs-5.0 LDAP-auth incompatibility is in scope (5.0 EOL).

## 4. `e2e_replica_set_scram_x509_internal_cluster`

- **Root cause:** `assert 2 == 1` — `assert_authentication_enabled()` defaults to
  `expected_num_deployment_auth_mechanisms=1`, but the automation config has 2
  (SCRAM-SHA-256 + MONGODB-X509). Commit `be59919dc` (CLOUDP-146110, Dec 2022) changed the
  shared fixture to `modes: ["SCRAM","X509"]` and updated only the `*_ic_manual_certs` siblings
  to expect 2; this test was missed. Orphaned since the EVG split (`d6adebfa0`).
- **Classification:** test defect (stale assertion). Operator behavior is correct.
- **Possible fixes:**
  - Pass `expected_num_deployment_auth_mechanisms=2` and re-enable (matches green
    `*_ic_manual_certs` siblings). (Preferred if auto-cert coverage wanted.)
  - Remove test + task — redundant with `*_ic_manual_certs` coverage.

## 5. `e2e_sharded_cluster_scram_x509_internal_cluster`

- **Root cause:** identical to #4 — `assert 2 == 1`, fixture `modes: ["SCRAM","X509"]`,
  `be59919dc` missed this test too. Orphaned since `d6adebfa0`.
- **Classification:** test defect (stale assertion).
- **Possible fixes:** same as #4 (`expected_num_deployment_auth_mechanisms=2` + re-enable, or
  remove as redundant).

## 6. `e2e_tls_multiple_different_ssl_configs`

- **Root cause:** fails in class setup — waits 120s for status message `"Not all certificates
  have been approved by Kubernetes CA"`, which **no longer exists anywhere in the operator**. It
  belonged to the manual CSR-approval flow removed in `CLOUDP-96586` (PR #1818, Sept 2021).
  Today the operator expects pre-created cert Secrets. Fixture pins MongoDB `4.4.0` (EOL).
  Orphaned since the Oct 2024 EVG split.
- **Classification:** stale test defect encoding a removed feature — can never pass.
- **Possible fixes:**
  - Rewrite test to the modern TLS flow (pre-create cert secrets, assert both RS Running +
    connectivity). (If mixed SSL/non-SSL coverage is wanted.)
  - Remove test + task (recommended) — CSR-approval half tests a deleted feature.
  - Re-enable as-is: impossible.

## 7. `e2e_multi_cluster_with_ldap`

- **Root cause:** `test_multi_replicaset_CLOUDP_229222` times out after ~1900s — MongoDBMulti
  stuck `Phase.Pending | StatefulSet not ready`. The RS never initializes (`NotYetInitialized`
  on all members); the agent deadlocks applying LDAP runtime setParameters on pinned
  `5.0.5-ent` (comment cites docker-library/mongo#606 for old EVG hosts, now `ubuntu2404`).
  The operator/recovery side worked (fixed AC reached Ops Manager). Both executions identical —
  deterministic.
- **Classification:** test/env rot — pins EOL 5.0.5-ent with a current agent; orphaned since the
  Oct 2024 split.
- **Possible fixes:**
  - Unpin version (use `custom_mdb_version`, e.g. 7.0.x) and re-enable if a patch run passes.
  - Remove test + task — LDAP coverage exists elsewhere.
  - Keep orphaned — zero CI cost, but dead code rots.

## 8. `e2e_multi_cluster_with_ldap_custom_roles`

- **Root cause:** `test_create_mongodb_multi_with_ldap` times out (1200s) — Pending
  `StatefulSet not ready`; pods never come up. Pins `5.0.5-ent` (docker-library/mongo#606
  comment); ran on `ubuntu2404-large`. Orphaned.
- **Classification:** test/env defect (pinned ancient 5.0.5 on newer hosts), not a product bug.
- **Possible fixes:**
  - Unpin version (restore `ensure_ent_version(custom_mdb_version)`) and re-enable if a patch
    run passes. (Preferred if coverage wanted.)
  - Remove test + task — custom-roles logic is covered single-cluster by
    `tests/authentication/replica_set_ldap_custom_roles.py`.
  - Keep orphaned — no CI cost.

---

## Common themes

- **Orphaned since ~Oct 2024** EVG config split (`d6adebfa0`) — never run, so bit-rot went
  unnoticed; they only surface when a patch schedules them explicitly.
- **Stale version pins** (5.0.x / 4.4) are a recurring trigger — several tests were written for
  older EVG hosts and pin ancient MongoDB versions that no longer converge with current agents
  on `ubuntu2404` hosts.
- **Two clean test-defect classes:** stale auth-mechanism-count assertions (#4, #5) and the
  removed CSR-approval flow (#6).
- **Environment mismatch:** tests that create organizations cannot run on Cloud Manager QA
  (#1, #2).

## Decision needed

For each task: **fix & re-enable** (where the test logic is still valid and only placement/version
needs updating) vs **remove test + task** (where the coverage is redundant or tests a removed/EOL
flow). This is an investigation ticket; no code changes are proposed for merge in this branch.
