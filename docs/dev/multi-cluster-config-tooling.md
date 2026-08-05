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
| 3 | Operator `MemberCluster` wiring + watch | CLOUDP-400899 | done | Build the per-cluster client map from `MemberCluster` CRs + credential Secrets. **Restart-based watch** chosen for this slice (mirrors the `OperatorConfig` watcher): the watcher restarts the operator on `MemberCluster` add/spec-change/delete. No-restart reactivity deferred to slice 9 (spike found it touches every controller's fan-out; `multicluster-runtime`'s `Provider`+`Engage(ctx)` is a candidate but its reconcile model is inverse to MCK's and it's pre-1.0). Discovery is CRs-if-present-else-legacy; legacy fallback tagged `TODO(m1kola): slice-3`. The member-cluster health checker (`memberwatch`) was made discovery-agnostic — it now sources per-cluster credentials from the in-memory `cluster.GetConfig()` rest.Config instead of the mounted kubeconfig file, so failover/health status works on both paths. |
| 4 | RBAC validation | CLOUDP-400899 | todo (after 5) | `RBACValid` condition validated against the `mongodb.com/rbac-version` annotation emitted by slice 1; startup gate + periodic re-check. **Deferred until after slice 5** — not a blocker for the E2E migration: the slice-3 operator has no runtime RBAC-version awareness, so member clusters set up by the new tooling work without it. Note: ensure that we generate public samples such as `public/samples/multi-cluster-cli-gitops/resources/rbac/cluster_scoped_member_cluster.yaml` correctly when bumping the operator version. It appears like `make precommit-full`/`make precommit` doesn't always (race?) generate the sample when the operator version is being bumped. |
| 5 | Migrate MC E2E to new tooling | CLOUDP-400899 | done | **Brought forward before slice 4.** Multi-cluster becomes day-2 config: install the operator single-cluster, then apply member RBAC (`generate-member-resources`) + registration (`generate-member-registration` → `MemberCluster` CRs) via new `conftest.py` helpers (`configure_multi_cluster_members`), replacing `run_kube_config_creation_tool` in the fixtures + direct callers. Recovery tests reworked to add/remove `MemberCluster` CRs (no `recover` CLI; `multi_cluster_cli_recover.py` renamed to `multi_cluster_member_add_remove.py`). The AppDB and sharded DR tests, which simulated an unhealthy cluster by editing the legacy member-list ConfigMap, now delete the failed cluster's `MemberCluster` CR + credential Secret instead (the sharded/AppDB controllers have no reachability health-check — a cluster is unhealthy purely by being absent from the operator's member map, which the CR deletion produces under both the current restart-based watch and a future hot reload). Apply the generated RBAC to **every** member cluster including central (do not `skip_central_cluster`) — validates the additive apply. Member configuration is unified in `conftest.py` for both the in-cluster and local operator (the old `prepare_local_e2e_run.sh` / `run_multi_cluster_kube_config_creator` pre-pytest registration is removed) — in both, pytest shares the operator's network vantage so the ambient kubeconfig carries operator-reachable addresses. **Follow-up: slice 9** — a local host-run operator currently exits on a `MemberCluster` CR change (the watcher cancels the manager context) and nothing restarts it, so the operator must be (re)started after the fixtures configure members. Mode "operator in-cluster + tests on host" relies on `kubefwd` and is out of scope. |
| 6 | Clean break | CLOUDP-400899 | in progress | Three stacked PRs so no PR is red on its own: **(1)** released-plugin E2E baseline — merged (#1435), see "Released baseline for upgrade tests" below; **(2)** public doc snippets off `multicluster setup` + `--set multiCluster.clusters` — open (#1442), see "Public doc snippets" below; **(3)** the removals — open (#1446). Removed: `multicluster setup`/`recover` and all of `pkg/kubectl-mongodb/common`; the legacy discovery + main.go fallback (`getMemberClusters`, the `pkg/multicluster` kubeconfig machinery; `membercluster.Discover` dropped its bool — no CRs = empty map = single-cluster); the member-list ConfigMap watch + `ConfigMapEventHandler`; `ValidateMemberClusterIsSubsetOfKubeConfig` (it failed open — a real `clusterSpecList` clusterNames ⊆ registered `MemberClusters` validation is a slice-4 candidate); the Helm values `multiCluster.clusters` + `multiCluster.kubeConfigSecretName`, the `kube-config-volume` mount they gated, and `values-multi-cluster.yaml`; `public/mongodb-kubernetes-multi-cluster.yaml` + its generation block (a multi-cluster install is now the standard install plus OperatorConfig/MemberCluster CRs). The gitops sample was **reworked, not deleted** — see "Key decisions". Remainders: `multiCluster.performFailOver` stays (owned by the Operator Config TD); the legacy baseline in `install_official_operator` is **permanent** — pinned pre-2.x baselines (MEKO→MCK, MCK 1.x→2.x upgrades) always need it (see "Released baseline for upgrade tests" below). The only 2.x-triggered change is the *unpinned* "latest published" baseline resolving to a 2.x chart, at which point the flow must be dispatched on the baseline chart's major instead of hardcoded (call-site comment in `conftest.py`). |
| 7 | Member-scoped workload ServiceAccounts | CLOUDP-400899 | done | `generate-member-resources` output now touches **nothing** from helm/OLM. The operator un-hardcoded the workload pod SA names (AppDB `construct/appdb_construction.go`, OM + backup `construct/opsmanager_construction.go`, database via a new `DatabaseStatefulSetOptions.ServiceAccountName` tier, mdbmulti via `WithServiceAccount`, mongot via a construction param driven by the existing `clusterWork.Local` discriminator): pods on member clusters run under `mck-member-<cluster>-{appdb,database-pods,ops-manager}`; single-cluster/legacy-central keeps the helm-install fixed names; user pod-template overrides still win. `database-roles.yaml` is **dual-mode** (one template, no new files): base render byte-identical (helm/OLM, fixed names); member render = member-scoped names + `mongodb.com/rbac-version`. Removed Helm values `operator.createResourcesServiceAccountsAndRoles` + `operator.createOperatorServiceAccount` (Operator Config TD "Settings to remove" — always deploy RBAC); gates dropped from `operator-sa`/`operator-roles-base`/`operator-roles-pvc-resize`/`operator-roles-clustermongodbroles` (its `enableClusterMongoDBRoles` gate stays). `_install_multi_cluster_operator` split: new-path registration vs `_install_released_multi_cluster_baseline` (keeps `prepare_multi_cluster_namespaces` with the released chart — permanent); the 5 direct new-path `prepare_multi_cluster_namespaces` call sites dropped (clusterwide tests pass explicit `member_clusters_watched_namespaces` instead of `"*"`); the 4 `createResourcesServiceAccountsAndRoles=false` workarounds + `extra_helm_args` plumbing removed; search snippets drop the flag. Discovery (`membercluster.Discover`) now also returns clusterName→metadata.name; a write-once startup registry in `pkg/resourcenames/member_cluster.go` resolves CR names for SA derivation — tagged `TODO(m1kola): slice-9`, **must be removed before the epic completes**. |
| 8 | RBAC de-duplication | CLOUDP-400899 | todo | Single source of truth for the operator's shared workload rules (services/secrets/configmaps/statefulsets/deployments/pods) so extending a permission is one edit, not two. Aim for: base role = shared + central-only (CRDs/operatorconfigs); member role = shared + member extras (serviceaccounts get, nodes, kube-system, /version). Mechanism left open (shared partial, restructured/parameterised template, generating member from the same source, …). Deferred deliberately — see below. Note: `database-roles.yaml` is already dual-mode since slice 7 (workload RBAC has one template serving base install and member render); the remaining dedup is `member-cluster-rbac.yaml` vs `operator-roles-base.yaml`. |
| 9 | No-restart `MemberCluster` reactivity (hot reload) | CLOUDP-400899 | todo | Make membership changes reactive **without** restarting the operator — the "later slice" referenced from slice 3, which currently restarts the operator on `MemberCluster` add/spec-change/delete. Candidate mechanism per slice 3: `multicluster-runtime`'s `Provider`+`Engage(ctx)` (reconcile model is inverse to MCK's and it's pre-1.0). This also resolves the **slice-5 local-dev caveat**: a host-run (`make run`) operator currently exits when the watcher cancels the manager context and nothing restarts it, so it must be (re)started after the E2E fixtures configure members day-2. Interim option if hot reload is not ready: wrap the local operator in a restart-loop/supervisor so it behaves like an in-cluster pod. Tagged `TODO(m1kola): slice-9` in `docker/mongodb-kubernetes-tests/tests/conftest.py`. Also removes the slice-7 resource-name registry (`pkg/resourcenames/member_cluster.go`, tagged `TODO(m1kola): slice-9`): with hot reload the clusterName→CR-metadata.name mapping used for member-scoped SA derivation must flow with the cluster objects themselves, not a startup-set global. |

**Dependencies:** 3 → {1, 2}; 4 → {1, 3}; 5 → {1, 2}; 6 → 5; 7 → 3 (needs multi-cluster reconcile working; can land any time after); 8 → {5, 7} (runs on the settled, E2E-covered shape); 9 → 3 (makes the slice-3 restart-based watch reactive; resolves the slice-5 local-operator caveat).

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
  `generate-member-resources --watched-namespaces="${OM_NAMESPACE},${MDB_NAMESPACE}"` per cluster: one
  member SA in `OM_NAMESPACE` with bindings in both namespaces.

Findings this surfaced:

- **GKE context names are not RFC 1123** (`gke_<project>_<zone>_<cluster>` — underscores), so they
  cannot be MemberCluster `metadata.name`/RBAC names. ra-02 sanitises (`${ctx//_/-}`) and passes the
  raw context as `--cluster-name` (`MemberCluster.spec.clusterName`, which has no RFC 1123 validation)
  so it still matches `clusterSpecList[].clusterName` in ra-06/07/08 workload CRs. The kind search
  tutorials need no such mapping (their contexts are already compliant).
- **The GKE variants needed no re-wiring.** `private_gke_code_snippets` already installs the branch
  chart: `scripts/dev/contexts/private_gke_code_snippets` exports
  `OPERATOR_HELM_CHART="${PROJECT_DIR}/helm_chart"` and dev-image values, and `configure_docker_auth`
  is already pulled in via `download_kube_tools`. `public_gke_code_snippets` explicitly keeps the
  released chart (`OPERATOR_HELM_CHART=""`), so it **stays red on this flow until MCK 2.x is
  released** — accepted: it is manual-only and its whole premise is testing the published flow.
- **The snippets deliberately do not wait for the operator's post-registration restart.** The slice-3
  watcher restarts the operator *in-place* (cancels the manager context; same pod, restartCount
  increments — no new Deployment rollout), so `kubectl rollout status` cannot observe it, and a
  restartCount watch (as the e2e harness's `wait_for_operator_pod_restart` does) is far too noisy for
  user-facing tutorials. It is also unnecessary: reconcile is level-based (CRs applied during the
  restart are picked up when the operator returns), and the operator's validating webhook is
  `failurePolicy: Ignore` (`pkg/webhook/setup.go`), so CR creation cannot be rejected mid-restart.
  Downstream snippet steps are either operator-independent or wait-based, so CI absorbs the delay.
  The wait that *is* load-bearing — for the `mck-member-*-token` Secret before
  `generate-member-registration` reads it — stays. Follow-up idea (tagged `TODO(m1kola): token-wait`
  in the snippets): make `generate-member-registration` poll for the token itself, removing the
  per-cluster wait loop from every caller.
- **`get_operator_helm_values` re-injected `multiCluster.clusters` in multi envs**
  (`scripts/funcs/operator_deployment:64-70`), which made the chart render the legacy
  `kube-config-volume` mount — and with no `multicluster setup` the kubeconfig Secret never exists,
  so the operator pod wedged in FailedMount. The pytest harness pops this value in
  `_install_multi_cluster_operator`; the bash snippet flow has no such pop, so
  `docs/search/1{2,3}-*/env_variables_e2e_private.sh` now filter it out of
  `OPERATOR_ADDITIONAL_HELM_VALUES`. PR 3 removed the injection and the filter lines with
  it; the released-baseline consumer `install_official_operator` now derives `multiCluster.clusters`
  locally for its pre-2.x baseline. (The GKE contexts never set
  `MEMBER_CLUSTERS`, so ra-02 is unaffected.)

## Workload RBAC: end-state (slice 7)

Member RBAC is **additive** and touches nothing from helm/OLM — both halves now satisfy this. The operator's own member RBAC (`mck-member-*`) always did. The **workload** RBAC does too since slice 7: `database-roles.yaml` is dual-mode — rendered by `helm install`/OLM it produces the fixed `mongodb-kubernetes-*` workload SAs/Role (base install; byte-identical to pre-slice-7 output), rendered by `generate-member-resources` (`memberCluster.enabled`) it produces member-scoped `mck-member-<cluster>-*` names annotated `mongodb.com/rbac-version`. The operator picks the matching SA per cluster at pod construction (member-scoped on member clusters, fixed on the legacy central/single-cluster path), so applying the generated output to the operator's own cluster is purely additive — no Helm ownership conflicts, which is why the `createResourcesServiceAccountsAndRoles=false` workarounds (and the Helm value itself) are gone.

## RBAC de-duplication (slice 8, deferred)

The operator's workload-management rules (services/secrets/configmaps/statefulsets/deployments/pods) live in **both** `helm_chart/templates/member-cluster-rbac.yaml` and `helm_chart/templates/operator-roles-base.yaml` — the operator needs them on its own cluster (single-cluster workloads) and on member clusters, so they're conceptually one set. They have **drifted**:
- the member role adds `deletecollection` on secrets/configmaps/services/statefulsets/deployments; the base role has it only on pods;
- PVCs are inline in the member role but in a separate `operator-roles-pvc-resize.yaml` role in the base install.

So extending a permission today means editing two places, with re-drift risk. **Do not fix this until anyone edits both sides in the meantime — keep them in sync.**

It is deferred to slice 8 (after 5 and 7) on purpose: the dedup's correctness is "the operator still has sufficient permissions on both cluster types", which is best proven by the **full E2E suite** (single-cluster exercises the base role; multi-cluster the member role) — not the current unit render tests, which only check YAML shape. Deferring until E2E runs against the new tooling also makes the proper single-canonical-set unification safe to apply to the **base** role (not just align the member role down), and lets the shape settle after slices 4/7 first. The canonical source for both sides is now the Helm templates (`helm_chart/templates/operator-roles-base.yaml` + `helm_chart/templates/member-cluster-rbac.yaml`), which the plugin embeds — the `pkg/kubectl-mongodb/common` Go copy that modelled the target split was removed in slice 6.

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
- **Operator-wiring reactivity** aims for no-restart; mechanism deferred to slice-3 planning.
- **Code layout**: keep `cmd/kubectl-mongodb/` purely CLI (flags, cobra wiring, stdout); all logic lives under `pkg/kubectl-mongodb/` (e.g. `pkg/kubectl-mongodb/memberresources` for slice 1) with the tests. Slice 2's registration logic goes in its own `pkg/kubectl-mongodb/...` package.
- **Gitops sample kept, rendered by the CLI itself**: `public/samples/multi-cluster-cli-gitops/` stays (users who cannot run the plugin still need checked-in RBAC YAML), and its two member RBAC samples are generated by running the CLI itself (`go run ./cmd/kubectl-mongodb multicluster generate-member-resources …`), so the sample and the CLI share exactly one rendering (the embedded Helm chart) and can never drift; the regeneration plumbing (`generate_files.sh regenerate_public_rbac_multi_cluster`, wired to the pre-commit hook) re-pointed from the deleted Go test to the CLI render. Central-cluster RBAC samples were dropped (central RBAC always ships with the operator install), and the recover-Job sample removed (recovery is re-applying `MemberCluster` CRs).

## Risks

- Helm SDK ↔ k8s alignment (resolved for slice 1; re-check on Helm bumps).
- Cross-arch plugin build (s390x/ppc64le) with the Helm SDK — pure Go, no cgo; smoke-build.

## References

- Base branch: `feature/mc-installation-ux`. Branches use the `iux-multi-cluster-` prefix.
