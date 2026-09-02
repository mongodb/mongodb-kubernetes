# Multi-Cluster Configuration Tooling & `MemberCluster` Wiring (MCK 2.x)

Living epic-overview doc for the multi-cluster half of the Installation UX epic
(CLOUDP-260547). It tracks the slice stack, dependencies, and risks. Detailed
per-slice implementation plans are produced just-in-time when each slice starts.

## Goal

MCK 2.x moves multi-cluster configuration from the **installation stage** to a unified
**configuration stage**:

- The operator discovers member clusters by watching `MemberCluster` CRs (each referencing
  a per-cluster credential Secret holding a single-context kubeconfig), replacing the MCK 1.x
  `mongodb-kubernetes-operator-member-list` ConfigMap + monolithic kubeconfig Secret.
- RBAC has a single source of truth (the Helm chart), embedded into the `kubectl mongodb`
  plugin and rendered by new subcommands. `multicluster setup`/`recovery` are removed.
- Member-cluster RBAC is validated at runtime via a `mongodb.com/rbac-version` annotation
  (`RBACValid` condition on `MemberCluster.status`).

## Approach: tooling-first

New tooling is added first, purely additively — the current operator is inert to its output,
so existing `setup`-driven E2E stay green (continuous CI signal). Then the operator is wired
to consume `MemberCluster` CRs (keeping a legacy fallback), then the legacy path is removed.

## Slice stack

| # | Slice | Jira | Status | Notes |
|---|-------|------|--------|-------|
| 1 | `generate-member-resources` command | CLOUDP-423293 | done | Embeds the Helm chart (Helm SDK); gated member-cluster RBAC templates; renders to stdout. Front-loads the Helm-SDK dependency risk. |
| 2 | `generate-member-registration` command | CLOUDP-423293 | done | Reads an SA token from a member cluster; emits a credential Secret + `MemberCluster` CR. No Helm SDK. |
| 3 | Operator `MemberCluster` wiring + watch | CLOUDP-400899 | done | Build the per-cluster client map from `MemberCluster` CRs + credential Secrets. **Restart-based watch** chosen for this slice (mirrors the `OperatorConfig` watcher): the watcher restarts the operator on `MemberCluster` add/spec-change/delete. Superseded by slice 9, which replaced the restart watch with the hot-reload reconciler (the `multicluster-runtime` rejection rationale is recorded there). Discovery is CRs-if-present-else-legacy; legacy fallback tagged `TODO(m1kola): slice-3`. The member-cluster health checker (`memberwatch`) was made discovery-agnostic — it now sources per-cluster credentials from the in-memory `cluster.GetConfig()` rest.Config instead of the mounted kubeconfig file, so failover/health status works on both paths. |
| 4 | RBAC validation | CLOUDP-400899 | todo (after 5) | `RBACValid` condition validated against the `mongodb.com/rbac-version` annotation emitted by slice 1; startup gate + periodic re-check. Lands on the slice-9 `membercluster.Reconciler` (single ordered writer; status writes don't rebuild provider entries thanks to generation tracking); the skip-reconcile-on-invalid signal flows to workload reconcilers via the provider snapshot, mirroring the existing healthy/unhealthy fan-out filter. **Open design question (from slice 9):** whether an RBAC-invalid cluster's provider entry is *present-but-flagged* (health checks and status reads can still reach it — preferred) or *absent* (breaks watch teardown and delete bookkeeping). Also: entry rebuilds (spec change / credential rotation) reset validity — register fail-closed (unvalidated) until the probe passes. **Credential Secret rotation and missing-Secret pickup fold into this slice**: the reconciler requeues MemberCluster CRs with a delay (periodic re-reconcile), so a rotated kubeconfig is picked up on the next cycle and a Secret created after its CR is retried without relying on error backoff — no credential-Secret watch needed. **Deferred until after slice 5** — not a blocker for the E2E migration: the slice-3 operator has no runtime RBAC-version awareness, so member clusters set up by the new tooling work without it. Note: ensure that we generate public samples such as `public/samples/multi-cluster-cli-gitops/resources/rbac/cluster_scoped_member_cluster.yaml` correctly when bumping the operator version. It appears like `make precommit-full`/`make precommit` doesn't always (race?) generate the sample when the operator version is being bumped. |
| 5 | Migrate MC E2E to new tooling | CLOUDP-400899 | done | **Brought forward before slice 4.** Multi-cluster becomes day-2 config: install the operator single-cluster, then apply member RBAC (`generate-member-resources`) + registration (`generate-member-registration` → `MemberCluster` CRs) via new `conftest.py` helpers (`configure_multi_cluster_members`), replacing `run_kube_config_creation_tool` in the fixtures + direct callers. Recovery tests reworked to add/remove `MemberCluster` CRs (no `recover` CLI; `multi_cluster_cli_recover.py` renamed to `multi_cluster_member_add_remove.py`). The AppDB and sharded DR tests, which simulated an unhealthy cluster by editing the legacy member-list ConfigMap, now delete the failed cluster's `MemberCluster` CR + credential Secret instead (the sharded/AppDB controllers have no reachability health-check — a cluster is unhealthy purely by being absent from the operator's member map, which the CR deletion produces under both the current restart-based watch and a future hot reload). Apply the generated RBAC to **every** member cluster including central (do not `skip_central_cluster`) — validates the additive apply. Member configuration is unified in `conftest.py` for both the in-cluster and local operator (the old `prepare_local_e2e_run.sh` / `run_multi_cluster_kube_config_creator` pre-pytest registration is removed) — in both, pytest shares the operator's network vantage so the ambient kubeconfig carries operator-reachable addresses. **Follow-up: slice 9** — resolved: the local host-run operator no longer exits on a `MemberCluster` CR change (the restart watcher was replaced by the hot-reload reconciler), so no manual restart is needed after the fixtures configure members. Mode "operator in-cluster + tests on host" relies on `kubefwd` and is out of scope. |
| 6 | Clean break | CLOUDP-400899 | done | Three stacked PRs so no PR is red on its own: **(1)** released-plugin E2E baseline — merged (#1435), see "Released baseline for upgrade tests" below; **(2)** public doc snippets off `multicluster setup` + `--set multiCluster.clusters` — open (#1442), see "Public doc snippets" below; **(3)** the removals — open (#1446). Removed: `multicluster setup`/`recover` and all of `pkg/kubectl-mongodb/common`; the legacy discovery + main.go fallback (`getMemberClusters`, the `pkg/multicluster` kubeconfig machinery; `membercluster.Discover` dropped its bool — no CRs = empty map = single-cluster); the member-list ConfigMap watch + `ConfigMapEventHandler`; `ValidateMemberClusterIsSubsetOfKubeConfig` (it failed open — a real `clusterSpecList` clusterNames ⊆ registered `MemberClusters` validation is a slice-4 candidate); the Helm values `multiCluster.clusters` + `multiCluster.kubeConfigSecretName`, the `kube-config-volume` mount they gated, and `values-multi-cluster.yaml`; `public/mongodb-kubernetes-multi-cluster.yaml` + its generation block (a multi-cluster install is now the standard install plus OperatorConfig/MemberCluster CRs). The gitops sample was **reworked, not deleted** — see "Key decisions". Remainders: `multiCluster.performFailOver` stays (owned by the Operator Config TD); the legacy baseline in `install_official_operator` is **permanent** — pinned pre-2.x baselines (MEKO→MCK, MCK 1.x→2.x upgrades) always need it (see "Released baseline for upgrade tests" below). The only 2.x-triggered change is the *unpinned* "latest published" baseline resolving to a 2.x chart, at which point the flow must be dispatched on the baseline chart's major instead of hardcoded (call-site comment in `conftest.py`). |
| 7 | Member-scoped workload ServiceAccounts | CLOUDP-400899 | done | `generate-member-resources` output now touches **nothing** from helm/OLM. The operator un-hardcoded the workload pod SA names (AppDB `construct/appdb_construction.go`, OM + backup `construct/opsmanager_construction.go`, database via a new `DatabaseStatefulSetOptions.ServiceAccountName` tier, mdbmulti via `WithServiceAccount`, mongot via a construction param driven by the existing `clusterWork.Local` discriminator): pods on member clusters run under `mck-member-<cluster>-{appdb,database-pods,ops-manager}`; single-cluster/legacy-central keeps the helm-install fixed names; user pod-template overrides still win. `database-roles.yaml` is **dual-mode** (one template, no new files): base render byte-identical (helm/OLM, fixed names); member render = member-scoped names + `mongodb.com/rbac-version`. Removed Helm values `operator.createResourcesServiceAccountsAndRoles` + `operator.createOperatorServiceAccount` (Operator Config TD "Settings to remove" — always deploy RBAC); gates dropped from `operator-sa`/`operator-roles-base`/`operator-roles-pvc-resize`/`operator-roles-clustermongodbroles` (its `enableClusterMongoDBRoles` gate stays). `_install_multi_cluster_operator` split: new-path registration vs `_install_released_multi_cluster_baseline` (keeps `prepare_multi_cluster_namespaces` with the released chart — permanent); the 5 direct new-path `prepare_multi_cluster_namespaces` call sites dropped (clusterwide tests pass explicit `member_clusters_watched_namespaces` instead of `"*"`); the 4 `createResourcesServiceAccountsAndRoles=false` workarounds + `extra_helm_args` plumbing removed; search snippets drop the flag. Discovery (`membercluster.Discover`) now also returns clusterName→metadata.name; a write-once startup registry in `pkg/resourcenames/member_cluster.go` resolves CR names for SA derivation — later removed in slice 9 PR 1, where the mapping moved onto `multicluster.Entry.ResourceName`. |
| 8 | RBAC de-duplication | CLOUDP-400899 | done | Single source of truth achieved via **dual-mode templates, no `define` helpers** (user decision): `operator-roles-base.yaml` now renders the shared workload rules **once, unconditionally** — output-identical in both modes — with central-only rules gated `if not memberMode`; member mode emits the renamed `mck-member-<c>-role-base` (was `-role`). `member-cluster-rbac.yaml` shrank to credentials + the new **`mck-member-<c>-role-multicluster`** extras role (`deletecollection` ×4 kinds — MC-only `DeleteAllOf` cleanup, owner references don't work cross-cluster — plus `serviceaccounts get` for the rbac-version self-read; each rule why-commented). `operator-roles-pvc-resize.yaml` is dual-mode too (audit established the 1.x inline-vs-separate difference was helm-vs-plugin drift, not functional: `HandlePVCResize` is one code path for all cluster types) and **pruned to least privilege `list,watch,update` in both modes** (code uses list+update; `watch` serves the cached-client informer) — the first slice in the stack to change the base install render (drops `get,patch,delete`; standalone goldens diff = exactly that). Accepted cost: the member binding mechanics (clusterScoped/workloadNamespaces → scope + bindings) are triplicated across the three templates — mechanics, not permissions; helper extraction deferred. Plugin whitelist +2 templates; recover-test cleanup list updated (new names, legacy `-role` tolerated 404). |
| 9 | No-restart `MemberCluster` reactivity (hot reload) | CLOUDP-400899 | done | Membership changes are reactive **without** restarting the operator. Two stacked PRs: **(1) `iux-multi-cluster-cluster-provider`** (#1485) — zero-behaviour-change refactor: thread-safe `Provider` registry (`Entry{Cluster, Client, ResourceName}`) in `pkg/multicluster`, all 8 reconcilers + telemetry read per-reconcile snapshots instead of constructor-time maps, slice-7 resource-name registry (`pkg/resourcenames/member_cluster.go`) removed (mapping flows on `Entry`/`multicluster.MemberCluster.ResourceName`). **(2) `iux-multi-cluster-hot-reload`** — the behavioural switch: a level-based `membercluster.Reconciler` (no status writes — slice 4 adds those) owns the per-cluster lifecycle: on CR add/spec-change it builds the rest.Config from the credential Secret, starts a `cluster.Cluster` with a per-entry context, and registers it via `provider.Set`; on CR delete it removes the entry and cancels the context (informers stop). The initial informer replay populates the provider, so the startup `Discover` and the restart watcher (`pkg/membercluster/watcher.go`) are gone. `Provider` gained `Delete` + engage hooks (`Hooks{OnAdd, OnRemove}`): each MC controller's `Add*` registers a hook that attaches its per-cluster watches to the new cluster (Watch-after-start, envtest-verified) and enqueues all CRs of its type (`EnqueueAll` over a `source.Channel` — expansion to a cluster where a CR owns nothing yet can't rely on watch replay); the memberwatch health checker is hook-driven (`AddCluster`/`RemoveCluster`, replacing the populate-only-if-empty hack). E2E: restart-waits replaced with no-restart assertions (restartCount unchanged across membership changes, in-cluster operator only); the DR suites' `test_operator_processes_member_removal` lost `@skip_if_local` (the local operator no longer dies on membership change — resolves the **slice-5 local-dev caveat**), and the sharded DR suite dropped its restart-driven AC-version `+1`. Removal semantics unchanged: resources on a removed cluster are abandoned. |
| 10 | `generate-member-resources` scope flags | CLOUDP-400899 | done | `--watched-namespaces` renamed to `--workload-namespaces` (never accepts `*` — hard error pointing at `--cluster-scoped`); new `--cluster-scoped` bool (member SA gets ClusterRole + single CRB; use when `operator.watchNamespace="*"`); new `--create-telemetry-roles` bool (default true; `false` renders zero cluster-scoped resources for customers who refuse them). One concern per flag: credential namespace (`--member-cluster-namespace`, help text fixed — it claimed to be the workload namespace), workload placement + narrowed bindings (list), operator-permission scope (bool). Collapses the clusterwide e2e double render to one invocation. Telemetry: `operator-roles-telemetry.yaml` is now dual-mode (member render = `mck-member-<c>-cluster-telemetry` + binding, `rbac-version`-annotated, gated by the same `operator.telemetry.installClusterRole`); the scope-conditional extras block in `member-cluster-rbac.yaml` is deleted (correct `nodes list` verb; the `namespaces list/watch` rule was dead — nothing in the operator lists namespaces on member clusters). The telemetry rules are verified byte-equivalent to the MCK 1.x member telemetry ClusterRole (`buildClusterRoleTelemetry`, deleted in #1446) and cover exactly the three member-cluster calls in `pkg/telemetry/cluster.go`. Member role is namespaced-rules-only in every mode — the boundary slice 8 dedups. Plugin values: `memberCluster.{clusterScoped,workloadNamespaces}`; the plugin no longer sets `operator.watchNamespace`. Base install renders byte-identical (`helm template` diff-verified). Samples regenerated (both gain the telemetry CR/CRB; cluster-scoped sample command now `--cluster-scoped`). **Flag names superseded in slice 11**: `--cluster-scoped` → `--operator-cluster-scoped`, `--create-telemetry-roles` → `--operator-telemetry`. |
| 11 | CLI flag naming pass | CLOUDP-400899 | done | Naming principle: **describe capabilities, not implementation details** (help texts state what the user gets, not the ClusterRole/binding/spec machinery; no Helm value references). Three renames: `--create-telemetry-roles` → `--operator-telemetry` (the verb "create" implied cluster mutation — the command only renders; the `operator-` prefix scopes it vs the product's other telemetry knobs: OperatorConfig telemetry block, chart `operator.telemetry.*`); `--cluster-scoped` → `--operator-cluster-scoped` (names *whose* scope changes — the operator's identity on the member cluster; workload RBAC is namespaced in every mode); `--cluster-name` → `--member-cluster-logical-name` (kills the `--member-cluster`/`--cluster-name` two-name ambiguity; joins the `--member-cluster-*` family alongside context/namespace). Family taxonomy: `--member-cluster-*` = member-cluster attributes, `--operator-*` = operator access/behaviour, `--workload-*` = workload placement; `--image-pull-secrets` stays unprefixed (serves both member and workload SAs). `--member-cluster-namespace` help polished to "the operator's credentials". Pure CLI-surface change: zero render-output change (chart values untouched; base render byte-identical). Harness python params renamed `cluster_scoped` → `operator_cluster_scoped` — the **legacy released-plugin path keeps `--cluster-scoped`** (that 1.x binary's flag is unchanged). Rejected alternatives (`--workload-cluster-name`, `--legacy-cluster-name`, `--cluster-wide`, `--telemetry`, …) recorded in the slice plan. |

**Dependencies:** 3 → {1, 2}; 4 → {1, 3}; 5 → {1, 2}; 6 → 5; 7 → 3 (needs multi-cluster reconcile working; can land any time after); 8 → {5, 7, 10} (runs on the settled, E2E-covered shape); 9 → 3 (makes the slice-3 restart-based watch reactive; resolves the slice-5 local-operator caveat); 10 → 7 (settles the member-side template shape slice 8 dedups); 11 → 10 (renames the flags slice 10 introduced).

## Released baseline for upgrade tests (slice 6, PR 1)

The MC upgrade tests start from a **released** operator chart, which only understands the
pre-`MemberCluster` discovery pair (monolithic kubeconfig Secret + `<operator-name>-member-list`
ConfigMap). Slice 6 deletes `multicluster setup` from the branch-built plugin, so the harness can no
longer produce that baseline with its own tooling. Two changes make the baseline self-sufficient ahead
of the removal:

**Terminology, because the versions collide.** Two product lines are in play. **MEKO**
(`mongodb/enterprise-operator`, images `mongodb-enterprise-operator-ubi`) is the pre-MCK enterprise
operator, reaching 1.33; it is what the repo's `LEGACY_*` constants refer to
(`LEGACY_OPERATOR_CHART`, `LEGACY_DEPLOYMENT_STATE_VERSION = 1.27.0`). **MCK**
(`mongodb/mongodb-kubernetes`) merged MEKO and MCO and started its own 1.0–1.9 line. So a bare "1.x" is
ambiguous, and `legacy` must not be used to mean "MCK 1.x". Naming here is explicit: `mck1x`.

- **A second, released plugin in the test image.** `scripts/release/kubectl_mongodb/download_released_kubectl_plugin.py`
  resolves the newest published release of a major line (`--major`, default 1 → **MCK 1.x**) from the
  public GitHub releases (no credentials — unlike the S3-based `download_kubectl_plugin.py`, whose AWS
  credentials the test pod does not have), verifies it against `checksums.txt`, and drops it next to the
  branch-built binary. The Dockerfile installs it as `multi-cluster-kube-config-creator-mck1x`;
  `conftest.run_legacy_kube_config_creation_tool` (the only caller, via `install_official_operator`)
  invokes it. The version is resolved at image-build time rather than pinned, so the baseline tracks the
  latest MCK 1.x; `RELEASED_KUBECTL_PLUGIN_VERSION` forces a specific one, and the resolved version is
  logged at image-build time.
  Evergreen: `download_released_multi_cluster_binary`, called from `build_test_image{,_arm}`.

  **One MCK 1.x plugin serves both MEKO and MCK 1.x baselines.** MCK 1.x inherited `multicluster setup`
  from MEKO, and the resources it creates are named purely from the flags passed (`--operator-name`,
  `--service-account`), so it provisions a MEKO baseline just as correctly — no MEKO-repo plugin needed.
  This is also strictly closer to MEKO's own tooling than the branch build the harness used previously.
- **Register `MemberCluster` CRs before the upgrade.** An MCK operator that boots with no
  `MemberCluster` CR falls back to single-cluster and cannot reconcile the pre-existing multi-cluster
  resources, so registration must precede the helm upgrade rather than follow it (which is what
  `_install_multi_cluster_operator` does for a fresh install). Each test applies the `MemberCluster` CRD
  (the legacy chart does not ship it) then calls `configure_multi_cluster_members`; this is idempotent
  with `_install_multi_cluster_operator`'s own registration step. Done in all three MC
  upgrade tests, each of which runs on **every PR patch**: `tests/upgrades/meko_mck_upgrade.py`,
  `tests/upgrades/appdb_tls_operator_upgrade_v1_32_to_mck.py`, and
  `tests/multicluster_appdb/multicluster_appdb_upgrade_downgrade_v1_27_to_mck.py`.

**Released baseline → released plugin.** The general rule this establishes: whenever the harness
installs a *released* operator, it provisions that operator's members with a *released* plugin of the
matching major. The branch-built plugin is only ever used for the branch operator.

So this path is **permanent**, not 2.x-cutover scaffolding, for two independent reasons: MEKO → MCK
upgrades are supported indefinitely, and the planned **MCK 1.x → 2.x** tests (latest released MCK 1.x →
branch build, mirroring today's MCK → MCK test) start from a 1.x baseline that also speaks only the
pre-`MemberCluster` flow. Both keep needing a released MCK 1.x `setup`. Note the 1.x → 2.x test needs no
new plugin plumbing — it is exactly the baseline this PR already provisions.

The rule has one **latent gap**, which bites only once a **2.x** baseline exists:
`install_official_operator` hardcodes the pre-`MemberCluster` flow for every multi-cluster baseline.
That is correct while every baseline is pre-2.x — today they all are, whether pinned to MEKO
(`LEGACY_OPERATOR_CHART`) or unpinned on `MCK_HELM_CHART` while the latest published MCK release is
still 1.x. The first MC test to start from a released **2.x** operator (e.g. a 2.x → 2.x upgrade after
2.x ships) instead needs `generate-member-resources`/`generate-member-registration` from a released 2.x
plugin, so the flow must be chosen from the baseline chart's major rather than hardcoded. The downloader
already takes `--major`, so the fetch side is ready; the dispatch is left as a comment at the call site
rather than built speculatively, since it cannot be exercised until a 2.x release exists.

The released MCK 1.x plugin is deliberately **not** wired into the local dev flow
(`prepare_local_e2e_run.sh` → `scripts/dev/prepare-multi-cluster/`): `install_official_operator` skips
legacy provisioning when `local_operator()` is set, so installing a released in-cluster operator as an
upgrade baseline is a CI-only flow and a local fetch would be dead weight.

## Public doc snippets (slice 6, PR 2)

The user-facing multi-cluster tutorials also ran on the legacy flow and are CI-executed, so they were
migrated ahead of the removals (`iux-multi-cluster-docs-snippets`):

- **Search tutorials** (`docs/search/12-search-rs-multi-cluster`, `13-search-sharded-multi-cluster`;
  run on every PR patch via `private_kind_multi_cluster_code_snippets`): `*_0100_install_operator.sh`
  lost the `multicluster setup` block, `multiCluster.clusters` and
  `operator.createOperatorServiceAccount=false` (keeping `createResourcesServiceAccountsAndRoles=false`,
  tagged `TODO(m1kola): slice-7`); a new `*_0110_configure_member_clusters.sh` runs
  `generate-member-resources` + `generate-member-registration` per cluster. The folders stay
  intentional twins.
- **Reference architecture** (`public/architectures/setup-multi-cluster/ra-02-setup-operator`; GKE,
  manual/staging only): `ra-02_0200_kubectl_mongodb_configure_multi_cluster.sh` (two `setup` calls)
  became `ra-02_0220_configure_member_clusters.sh`, reordered to run **after** the operator install —
  registration applies `MemberCluster` CRs to central and needs the CRD the chart ships. The two
  `setup` calls (one per watched namespace) collapse into a single
  `generate-member-resources --workload-namespaces="${OM_NAMESPACE},${MDB_NAMESPACE}"` per cluster: one
  member SA in `OM_NAMESPACE` with bindings in both namespaces.

Findings this surfaced:

- **GKE context names are not RFC 1123** (`gke_<project>_<zone>_<cluster>` — underscores), so they
  cannot be MemberCluster `metadata.name`/RBAC names. ra-02 sanitises (`${ctx//_/-}`) and passes the
  raw context as `--cluster-name` (renamed `--member-cluster-logical-name` in slice 11; `MemberCluster.spec.clusterName`, which has no RFC 1123 validation)
  so it still matches `clusterSpecList[].clusterName` in ra-06/07/08 workload CRs. The kind search
  tutorials need no such mapping (their contexts are already compliant).
- **The GKE variants needed no re-wiring.** `private_gke_code_snippets` already installs the branch
  chart: `scripts/dev/contexts/private_gke_code_snippets` exports
  `OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"` and dev-image values, and `configure_docker_auth`
  is already pulled in via `download_kube_tools`. `public_gke_code_snippets` explicitly keeps the
  released chart (`OPERATOR_HELM_CHART=""`), so it **stays red on this flow until MCK 2.x is
  released** — accepted: it is manual-only and its whole premise is testing the published flow.
- **The snippets deliberately do not wait for the operator to react to registration.** Membership
  changes are picked up asynchronously (since slice 9 the operator hot-reloads them without even a
  restart), reconcile is level-based (CRs applied before the operator catches up are picked up on
  the next reconcile), and the operator's validating webhook is `failurePolicy: Ignore`
  (`pkg/webhook/setup.go`). Downstream snippet steps are either operator-independent or wait-based,
  so CI absorbs the pickup delay.
  The wait that *is* load-bearing — for the `mck-member-*-token` Secret before
  `generate-member-registration` reads it — has since moved **into** the command
  (`iux-multi-cluster-token-wait`, follow-up after slice 6): `generate-member-registration` polls
  for the Secret's `token`/`ca.crt` keys for up to a minute (`DefaultTokenWaitTimeout`), so the
  per-cluster wait loops are gone from every caller — the snippets and the E2E harness
  (`_wait_for_member_sa_token` deleted).
- **`get_operator_helm_values` re-injected `multiCluster.clusters` in multi envs**
  (`scripts/funcs/operator_deployment:64-70`), which made the chart render the legacy
  `kube-config-volume` mount — and with no `multicluster setup` the kubeconfig Secret never exists,
  so the operator pod wedged in FailedMount. The pytest harness pops this value in
  `_install_multi_cluster_operator`; the bash snippet flow has no such pop, so
`docs/search/1{2,3}-*/env_variables_e2e_private.sh` now filter it out of
   `OPERATOR_ADDITIONAL_HELM_VALUES`. PR 3 removed the injection; the filter lines
   survived it and were dropped in the clean-break follow-ups
   (`iux-multi-cluster-clean-break-followups`), restoring the plain comma join the other
   snippet env files use. The released-baseline consumer `install_official_operator` now
   derives `multiCluster.clusters` locally for its pre-2.x baseline. (The GKE contexts
   never set `MEMBER_CLUSTERS`, so ra-02 is unaffected.)

## Workload RBAC: end-state (slice 7)

Member RBAC is **additive** and touches nothing from helm/OLM — both halves now satisfy this. The operator's own member RBAC (`mck-member-*`) always did. The **workload** RBAC does too since slice 7: `database-roles.yaml` is dual-mode — rendered by `helm install`/OLM it produces the fixed `mongodb-kubernetes-*` workload SAs/Role (base install; byte-identical to pre-slice-7 output), rendered by `generate-member-resources` (`memberCluster.enabled`) it produces member-scoped `mck-member-<cluster>-*` names annotated `mongodb.com/rbac-version`. The operator picks the matching SA per cluster at pod construction (member-scoped on member clusters, fixed on the legacy central/single-cluster path), so applying the generated output to the operator's own cluster is purely additive — no Helm ownership conflicts, which is why the `createResourcesServiceAccountsAndRoles=false` workarounds (and the Helm value itself) are gone.

## RBAC de-duplication: end-state (slice 8)

The operator's workload-management rules now have a single source of truth per concern, all
in the Helm templates (which the plugin embeds):

- **Shared namespaced workload rules** (services/secrets/configmaps/statefulsets/deployments/pods)
  live once, unconditionally, in `operator-roles-base.yaml`. Base mode appends the
  central-only rules (CRD groups, `operatorconfigs`/`memberclusters`, cluster-wide
  `namespaces list/watch`); member mode renders the same shared rules as
  `mck-member-<c>-role-base`. Extending a shared permission is one edit again.
- **Multi-cluster-only rules** (`deletecollection` on the four kinds — used only by the
  MC `DeleteAllOf` cleanup paths, since owner references don't work across clusters — and
  `serviceaccounts get` for the rbac-version self-read) live in
  `mck-member-<c>-role-multicluster` in `member-cluster-rbac.yaml`, with why-comments.
- **PVC-resize rules** are least-privilege (`list,watch,update`) in the dual-mode
  `operator-roles-pvc-resize.yaml`; **telemetry** in the dual-mode
  `operator-roles-telemetry.yaml` (slice 10); **workload RBAC** in the dual-mode
  `database-roles.yaml` (slice 7).

Deliberately **not** done: Helm `define`/`include` helpers for the member binding
mechanics (user decision), so the clusterScoped/workloadNamespaces → scope/bindings
computation is duplicated across `operator-roles-base.yaml`, `member-cluster-rbac.yaml`
and `operator-roles-pvc-resize.yaml`. Correctness of the dedup was gate-kept by render
diffs (base render byte-identical except the deliberate PVC verb prune) plus the full E2E
suite (single-cluster exercises the base role, multi-cluster the member roles).

## Key decisions

- **Chart embedded as a Go package** (`helm_chart/embed.go`, `package helmchart`), imported by
  the plugin. `go run ./cmd/kubectl-mongodb` always embeds the live chart — no copy step, no
  drift. The `//go:embed` pattern uses `all:` so `templates/_helpers.tpl` is included; a
  `.helmignore` keeps `*.go` out of `helm package`.
- **Helm SDK** pinned to `helm.sh/helm/v3 v3.18.6` (its k8s pin matches the repo's `v0.33`).
- **Member RBAC naming** `mck-member-<cluster-name>-*`, deliberately decoupled from
  `.Values.operator.name` (so it's unaffected by operator-name unification — see below) and
  reconstructable by the operator from the `MemberCluster` CR's metadata.name.
- **Operator-name unification** is out of scope for *this* slice stack but is a decided goal owned
  by the *Introduction of the Operator Config* TD ("we need to unify" the single- vs multi-cluster
  `operator.name` — `mongodb-kubernetes-operator` vs `mongodb-kubernetes-operator-multi-cluster`;
  timing deferred). When it lands it affects the MC E2E harness: `MULTI_CLUSTER_OPERATOR_NAME`
  (`tests/constants.py`), the `operator.name` Helm value set in `_install_multi_cluster_operator`,
  and the name `wait_for_operator_ready` polls on. The MCK 1.x→2.x upgrade-path TD ("Operator Name
  Unification") also depends on it. Not tracked as a slice here; flagged so the "decoupled" note
  above isn't misread as unification being rejected.
- **Operator-wiring reactivity is no-restart** (slice 9): a hand-rolled `multicluster.Provider` registry + level-based `MemberCluster` reconciler with engage hooks. Rejected: `sigs.k8s.io/multicluster-runtime` — a custom CRD-discovery provider is needed either way, real value requires an `mcbuilder`/`mcreconcile` migration of every MC controller, and its pre-1.0 API churn would sit on the operator's most critical wiring. Our provider mirrors its seam (`Provider.Get` ≈ registry, `Aware.Engage` ≈ hooks with lifecycle-tied per-entry contexts), so a later migration is an adapter over the registry, not a rewrite.
- **Code layout**: keep `cmd/kubectl-mongodb/` purely CLI (flags, cobra wiring, stdout); all logic lives under `pkg/kubectl-mongodb/` (e.g. `pkg/kubectl-mongodb/memberresources` for slice 1) with the tests. Slice 2's registration logic goes in its own `pkg/kubectl-mongodb/...` package.
- **Gitops sample kept, rendered by the CLI itself**: `public/samples/multi-cluster-cli-gitops/` stays (users who cannot run the plugin still need checked-in RBAC YAML), and its two member RBAC samples are generated by running the CLI itself (`go run ./cmd/kubectl-mongodb multicluster generate-member-resources …`), so the sample and the CLI share exactly one rendering (the embedded Helm chart) and can never drift; the regeneration plumbing (`generate_files.sh regenerate_public_rbac_multi_cluster`, wired to the pre-commit hook) re-pointed from the deleted Go test to the CLI render. Central-cluster RBAC samples were dropped (central RBAC always ships with the operator install), and the recover-Job sample removed (recovery is re-applying `MemberCluster` CRs).

## Risks

- Helm SDK ↔ k8s alignment (resolved for slice 1; re-check on Helm bumps).
- Cross-arch plugin build (s390x/ppc64le) with the Helm SDK — pure Go, no cgo; smoke-build.

## References

- Base branch: `feature/mc-installation-ux`. Branches use the `iux-multi-cluster-` prefix.
