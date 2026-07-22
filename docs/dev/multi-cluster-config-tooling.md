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
| 1 | `generate-member-resources` command | CLOUDP-423293 | in progress | Embeds the Helm chart (Helm SDK); gated member-cluster RBAC templates; renders to stdout. Front-loads the Helm-SDK dependency risk. |
| 2 | `generate-member-registration` command | CLOUDP-423293 | in progress | Reads an SA token from a member cluster; emits a credential Secret + `MemberCluster` CR. No Helm SDK. |
| 3 | Operator `MemberCluster` wiring + watch | CLOUDP-400899 | in progress | Build the per-cluster client map from `MemberCluster` CRs + credential Secrets. **Restart-based watch** chosen for this slice (mirrors the `OperatorConfig` watcher): the watcher restarts the operator on `MemberCluster` add/spec-change/delete. No-restart reactivity deferred to slice 9 (spike found it touches every controller's fan-out; `multicluster-runtime`'s `Provider`+`Engage(ctx)` is a candidate but its reconcile model is inverse to MCK's and it's pre-1.0). Discovery is CRs-if-present-else-legacy; legacy fallback tagged `TODO(m1kola): slice-3`. The member-cluster health checker (`memberwatch`) was made discovery-agnostic — it now sources per-cluster credentials from the in-memory `cluster.GetConfig()` rest.Config instead of the mounted kubeconfig file, so failover/health status works on both paths. |
| 4 | RBAC validation | CLOUDP-400899 | todo (after 5) | `RBACValid` condition validated against the `mongodb.com/rbac-version` annotation emitted by slice 1; startup gate + periodic re-check. **Deferred until after slice 5** — not a blocker for the E2E migration: the slice-3 operator has no runtime RBAC-version awareness, so member clusters set up by the new tooling work without it. |
| 5 | Migrate MC E2E to new tooling | CLOUDP-400899 | in progress | **Brought forward before slice 4.** Multi-cluster becomes day-2 config: install the operator single-cluster, then apply member RBAC (`generate-member-resources`) + registration (`generate-member-registration` → `MemberCluster` CRs) via new `conftest.py` helpers (`configure_multi_cluster_members`), replacing `run_kube_config_creation_tool` in the fixtures + direct callers. Recovery tests reworked to add/remove `MemberCluster` CRs (no `recover` CLI; `multi_cluster_cli_recover.py` renamed to `multi_cluster_member_add_remove.py`). The AppDB and sharded DR tests, which simulated an unhealthy cluster by editing the legacy member-list ConfigMap, now delete the failed cluster's `MemberCluster` CR + credential Secret instead (the sharded/AppDB controllers have no reachability health-check — a cluster is unhealthy purely by being absent from the operator's member map, which the CR deletion produces under both the current restart-based watch and a future hot reload). Apply the generated RBAC to **every** member cluster including central (do not `skip_central_cluster`) — validates the additive apply. Member configuration is unified in `conftest.py` for both the in-cluster and local operator (the old `prepare_local_e2e_run.sh` / `run_multi_cluster_kube_config_creator` pre-pytest registration is removed) — in both, pytest shares the operator's network vantage so the ambient kubeconfig carries operator-reachable addresses. **Follow-up: slice 9** — a local host-run operator currently exits on a `MemberCluster` CR change (the watcher cancels the manager context) and nothing restarts it, so the operator must be (re)started after the fixtures configure members. Mode "operator in-cluster + tests on host" relies on `kubefwd` and is out of scope. |
| 6 | Clean break | CLOUDP-400899 | todo | Remove `setup`/`recovery`, the legacy discovery + fallback, and dead `common.go` RBAC/kubeconfig code. Also remove `multiCluster.clusters` and `multiCluster.kubeConfigSecretName` (still set in the `operator-installation-config` ConfigMap for `install_official_operator`'s legacy baseline, and popped for the new path in `_install_multi_cluster_operator`) — plus the `kube-config-volume` mount they gate in `helm_chart/templates/operator.yaml:59-61,263-267`. (Slice 5 already stopped `scripts/funcs/operator_deployment` forcing `operator.createOperatorServiceAccount=false` for multi, so the operator installs its base RBAC via Helm.) |
| 7 | Member-scoped workload ServiceAccounts | CLOUDP-400899 | todo | End-state so `generate-member-resources` output touches **nothing** from helm/OLM. Un-hardcode the workload pod SA names in the operator (`construct/appdb_construction.go:500`, `construct/opsmanager_construction.go:480`; database SA already per-CR overridable) so pods on member clusters run under member-scoped SAs; emit member-scoped workload RBAC instead of the interim fixed-name `database-roles.yaml`. Single-cluster keeps using the helm-install SAs. |
| 8 | RBAC de-duplication | CLOUDP-400899 | todo | Single source of truth for the operator's shared workload rules (services/secrets/configmaps/statefulsets/deployments/pods) so extending a permission is one edit, not two. Aim for: base role = shared + central-only (CRDs/operatorconfigs); member role = shared + member extras (serviceaccounts get, nodes, kube-system, /version). Mechanism left open (shared partial, restructured/parameterised template, generating member from the same source, …). Deferred deliberately — see below. |
| 9 | No-restart `MemberCluster` reactivity (hot reload) | CLOUDP-400899 | todo | Make membership changes reactive **without** restarting the operator — the "later slice" referenced from slice 3, which currently restarts the operator on `MemberCluster` add/spec-change/delete. Candidate mechanism per slice 3: `multicluster-runtime`'s `Provider`+`Engage(ctx)` (reconcile model is inverse to MCK's and it's pre-1.0). This also resolves the **slice-5 local-dev caveat**: a host-run (`make run`) operator currently exits when the watcher cancels the manager context and nothing restarts it, so it must be (re)started after the E2E fixtures configure members day-2. Interim option if hot reload is not ready: wrap the local operator in a restart-loop/supervisor so it behaves like an in-cluster pod. Tagged `TODO(m1kola): slice-9` in `docker/mongodb-kubernetes-tests/tests/conftest.py`. |

**Dependencies:** 3 → {1, 2}; 4 → {1, 3}; 5 → {1, 2}; 6 → 5; 7 → 3 (needs multi-cluster reconcile working; can land any time after); 8 → {5, 7} (runs on the settled, E2E-covered shape); 9 → 3 (makes the slice-3 restart-based watch reactive; resolves the slice-5 local-operator caveat).

## Interim vs end-state: workload RBAC

Member RBAC must be **additive** and never touch helm/OLM-provided resources. The operator's own member RBAC (`mck-member-*`) already satisfies this. The **workload** pod SAs do not yet: they are fixed-name and hard-coded in pod construction, so slice 1's `generate-member-resources` emits `database-roles.yaml` (fixed names), which re-applies over the helm/OLM copies on the operator's own cluster — a harmless idempotent apply, but not truly additive. Slice 7 reaches the end-state by making the operator use member-scoped workload SAs on member clusters. Until then the interim is tagged `TODO(m1kola): slice-1` in `pkg/kubectl-mongodb/memberresources`.

## RBAC de-duplication (slice 8, deferred)

The operator's workload-management rules (services/secrets/configmaps/statefulsets/deployments/pods) live in **both** `helm_chart/templates/member-cluster-rbac.yaml` and `helm_chart/templates/operator-roles-base.yaml` — the operator needs them on its own cluster (single-cluster workloads) and on member clusters, so they're conceptually one set. They have **drifted**:
- the member role adds `deletecollection` on secrets/configmaps/services/statefulsets/deployments; the base role has it only on pods;
- PVCs are inline in the member role but in a separate `operator-roles-pvc-resize.yaml` role in the base install.

So extending a permission today means editing two places, with re-drift risk. **Do not fix this until anyone edits both sides in the meantime — keep them in sync.**

It is deferred to slice 8 (after 5 and 7) on purpose: the dedup's correctness is "the operator still has sufficient permissions on both cluster types", which is best proven by the **full E2E suite** (single-cluster exercises the base role; multi-cluster the member role) — not the current unit render tests, which only check YAML shape. Deferring until E2E runs against the new tooling also makes the proper single-canonical-set unification safe to apply to the **base** role (not just align the member role down), and lets the shape settle after slices 4/7 first. The Go source already models the target split (`getMemberRules()` shared; `buildCentralEntityRole` = central + shared; `buildMemberEntityRole` = shared + member extras) in `pkg/kubectl-mongodb/common/common.go`.

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

## Risks

- Helm SDK ↔ k8s alignment (resolved for slice 1; re-check on Helm bumps).
- Cross-arch plugin build (s390x/ppc64le) with the Helm SDK — pure Go, no cgo; smoke-build.

## References

- Base branch: `feature/mc-installation-ux`. Branches use the `iux-multi-cluster-` prefix.
