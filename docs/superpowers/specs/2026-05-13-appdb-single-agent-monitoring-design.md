# AppDB Single-Agent Monitoring Design

**Date:** 2026-05-13  
**Branch:** decuple-plan-10b-appdb-construct-simplify (builds on top of it)  
**Reference:** [Attack EA doc — Starting AppDB monitoring immediately](https://docs.google.com/document/d/16nH4kSogQZGBd9AxQ5NPmgCvQSjzVsDKO_wYwtyrBcs/edit?tab=t.pd4m0hzyg3i#heading=h.4a13mnbah4g5)  
**PoC branch:** `maciejk/attack-ea` (commit `4ef2de4f4`)

---

## Problem

AppDB uses a headless automation agent for database management and a separate monitoring agent container for sending metrics to Ops Manager. Starting monitoring requires a two-step reconciliation: the StatefulSet is first deployed with only the automation container, then updated (causing a pod restart) to add the monitoring container once Ops Manager has created a project and issued an agent API key.

This increases startup time, complicates StatefulSet construction, and couples the pod lifecycle to the OM project-creation sequence.

---

## Solution

Run automation and monitoring in a single agent process. The mongodb-agent binary already supports this: when its automation config contains a `monitoringVersions` entry whose `hostname` matches the current pod, the monitoring module starts automatically. Connection credentials (`mmsGroupId`, `mmsApiKey`) and TLS parameters are passed via `additionalParams` on that entry — no command-line flag changes, no pod restart, compatible with all existing agent versions.

**No feature flag.** The monitoring container is removed unconditionally. The existing `OPS_MANAGER_MONITOR_APPDB` operator env var (via `GlobalMonitoringSettingEnabled()`) continues to let operators disable monitoring entirely.

---

## Architecture

### Before

```
Pod
├── mongodb-agent                  (automation, headless, -cluster flag)
└── mongodb-agent-monitoring       (monitoring, -mmsGroupId/-mmsApiKey flags)
    ├── monitoring-automation-config secret volume
    ├── monitoring-automation-config-goal-version configmap volume
    └── agent-api-key secret volume
```

### After

```
Pod
└── mongodb-agent                  (automation + monitoring, headless, -cluster flag)
    └── monitoring enabled via monitoringVersions.additionalParams in main automation config
```

### Startup sequence

1. Operator creates automation config **without** `monitoringVersions` — pods start, agent manages mongod only.
2. Ops Manager becomes ready → operator creates project, generates `agentApiKey`.
3. Operator updates automation config: adds `monitoringVersions` entries with `additionalParams`.
4. Agent reloads config → monitoring module starts → metrics appear in OM. **Zero pod restarts.**

---

## Automation config change

When `ShouldEnableMonitoring(podVars)` is true, each process entry in the automation config gets a corresponding `monitoringVersions` entry. The `baseUrl` field is populated by the existing `setBaseUrlForAgents` call that already iterates all `MonitoringVersions` entries — no new code needed for that field.

The `name` field uses the existing `om.MonitoringAgentDefaultVersion` constant (same value as today's separate monitoring AC). The agent does not enforce a matching binary version in single-process mode.

```json
"monitoringVersions": [{
  "hostname": "<pod-fqdn>",
  "name":     "<om.MonitoringAgentDefaultVersion>",
  "baseUrl":  "<set by setBaseUrlForAgents>",
  "additionalParams": {
    "mmsGroupId": "<projectId>",
    "mmsApiKey":  "<agentApiKey>"
  }
}]
```

With TLS, additional fields are added to `additionalParams`:

```json
{
  "useSslForAllConnections":              "true",
  "sslTrustedServerCertificates":         "<ca-cert-path>",
  "sslRequireValidMMSServerCertificates": "<podVars.SSLRequireValidMMSServerCertificates>",
  "sslClientCertificate":                 "<client-cert-path>"
}
```

`sslRequireValidMMSServerCertificates` is driven by `podVars.SSLRequireValidMMSServerCertificates` (not hardcoded), preserving existing behavior.

When `ShouldEnableMonitoring` is false (monitoring globally disabled, or OM not yet ready), `automationConfig.MonitoringVersions` is explicitly set to `nil` to clear any previously written entries.

**Scale-down cleanup:** `configureMonitoring` builds `MonitoringVersions` by iterating `ac.Processes`. Entries for removed replicas are naturally absent from the next AC publish, so stale entries are pruned automatically.

---

## Code changes

### `env.PodEnvVars`

Add `AgentAPIKey string` field. Populated in:

- `tryConfigureMonitoringInOpsManager` — from the return value of `ensureAppDbAgentApiKey` (the agent API key, not the OM admin key). The return statement must explicitly include `AgentAPIKey: agentApiKey` — this value is already computed at that call site but currently not placed in the returned struct.
- `readExistingPodVars` — read from the agent API key secret via `secret.ReadKey(ctx, memberClient, util.OmAgentApiKey, kube.ObjectKey(namespace, agents.ApiKeySecretName(projectId)))`. Note argument order: field key before object key, matching the real `secret.ReadKey` signature. This fixes the PoC bug where the OM admin credentials were used instead of the agent API key.

In multi-cluster deployments `readExistingPodVars` reads from the first member cluster's client, consistent with how the projectID configmap is read today. In a pathological case where the first member cluster is temporarily unavailable, `AgentAPIKey` will be empty and monitoring will not be updated that reconcile; it self-corrects when the cluster recovers.

### `appdbreplicaset_controller.go`

**`buildAppDbAutomationConfig` signature:**

```go
// Before
func (r *...) buildAppDbAutomationConfig(ctx, opsManager, acType agentType, prometheusCertHash, memberClusterName, log)

// After
func (r *...) buildAppDbAutomationConfig(ctx, opsManager, podVars *env.PodEnvVars, prometheusCertHash, memberClusterName, log)
```

The `acType` parameter is removed. Whether monitoring is configured is determined solely by `ShouldEnableMonitoring(podVars)` inside the function.

**`configureMonitoring` (formerly `addMonitoring`) signature:**

```go
// Before
func configureMonitoring(ac *automationconfig.AutomationConfig, log *zap.SugaredLogger, tls bool)

// After
func configureMonitoring(ac *automationconfig.AutomationConfig, log *zap.SugaredLogger, tls bool, projectID string, agentAPIKey string, requireValidCert bool)
```

`mmsGroupId` and `mmsApiKey` are always added to `additionalParams` regardless of TLS. TLS fields are added on top when `tls == true`. `requireValidCert` maps to `sslRequireValidMMSServerCertificates`.

**Removed:**

| Removed | Notes |
|---|---|
| `deployMonitoringAgentAutomationConfig()` | Monitoring AC merged into main AC |
| `getLegacyMonitoringAgentVersion()` | No separate legacy monitoring image |
| `appdbOpts.LegacyMonitoringAgentImage` | Removed from `AppDBStatefulSetOptions` |
| Publishing monitoring AC version configmap | No separate monitoring configmap |
| `monitoring` `agentType` constant and all its branches | Single AC covers both roles |

**`MonitoringAgent.StartupParameters` CRD field:** this field becomes effectively unused — monitoring is now fully driven by `additionalParams` in the AC, not by container command-line flags. The field is not removed from the CRD (backwards compatibility) but a warning is logged when it is non-empty: `"spec.appDB.monitoringAgent.startupParameters is set but has no effect; monitoring is now configured via the automation config"`.

### `construct/appdb_construction.go`

| Removed | Notes |
|---|---|
| `addMonitoringContainer()` (~280 lines) | Entire function deleted |
| `removeContainerByName()` call in `AppDbStatefulSet` | No monitoring container to remove |
| `monitoringAgentContainerName` container | Gone |
| Monitoring AC secret volume + mount | Gone |
| Monitoring AC goal-version configmap + mount | Gone |
| `AgentAPIKeyVolumeName` volume + mount (non-Vault path) | Gone — API key now in AC `additionalParams`; bash preamble no longer needs it |
| `tmpSubpathName` | Gone |
| `monitoringAgentHealthStatusFilePathValue` | Gone |
| `ShouldEnableMonitoring` conditional in `AppDbStatefulSet` | Gone — StatefulSet always has one agent container |

`AutomationAgentCommand` is called with `withAgentAPIKeyExport=false` for AppDB — the bash preamble no longer exports `AGENT_API_KEY`.

**Vault path (`vaultModification`):** `appDBSecretsToInject.AgentApiKey` is no longer set for AppDB. The agent API key is now embedded in the AC `additionalParams` (the AC itself is injected by Vault). Removing this field eliminates an unnecessary Vault sidecar fetch.

**Sanitisation:** any container named `monitoringAgentContainerName` in a user-supplied `podTemplateSpec` is stripped with a warning log, to handle clusters upgrading from the two-container model.

### `construct/appdb_agent_command.go`

`withAgentAPIKeyExport` path still exists for non-AppDB callers (no change). The AppDB call site changes the argument to `false`.

---

## `ShouldEnableMonitoring` stays

`ShouldEnableMonitoring(podVars)` — which checks both `GlobalMonitoringSettingEnabled()` and `podVars.ProjectID != ""` — continues to guard:
- Whether `monitoringVersions` are populated in the automation config.
- The toggle-off path: when it returns false, `MonitoringVersions` is explicitly cleared.

The StatefulSet always has exactly **one** agent container regardless of this flag.

---

## Error handling

| Situation | Behaviour |
|---|---|
| OM not yet ready (first boot) | AC deployed without `monitoringVersions`. Monitoring added on next successful reconcile. Zero pod restarts. |
| OM goes down after monitoring was configured | `readExistingPodVars` returns cached project ID + agent API key from the projectID configmap and agent API key secret. `monitoringVersions` remain in AC. No StatefulSet change. |
| `readExistingPodVars` cannot reach first member cluster | `AgentAPIKey` is empty; `ShouldEnableMonitoring` returns false (no project ID either in this case); AC is deployed without `monitoringVersions` until cluster recovers. |
| Stale/wrong agent API key in AC | Monitoring module rejects auth; retries on next AC reload. Operator corrects on next reconcile when OM is reachable. |
| `GlobalMonitoringSettingEnabled()` toggled off after monitoring was running | `MonitoringVersions` explicitly set to nil in AC on next reconcile. Monitoring module stops. No pod restart. |
| `GlobalMonitoringSettingEnabled()` toggled back on | `monitoringVersions` re-populated. No pod restart. |
| User pod template names `mongodb-agent-monitoring` container | Stripped with a warning during merge; not deployed. |
| `MonitoringAgent.StartupParameters` is non-empty | Warning logged; field has no effect in new model. |

---

## Testing

### Unit tests

| Scenario | Location | What to assert |
|---|---|---|
| Monitoring enabled, non-TLS | `appdbreplicaset_controller_test.go` | AC has `monitoringVersions` with `mmsGroupId`, `mmsApiKey`; no TLS fields |
| Monitoring enabled, TLS | `appdbreplicaset_controller_test.go` | AC has full `additionalParams` including all TLS fields; `sslRequireValidMMSServerCertificates` driven by `podVars` |
| Monitoring disabled from start (`GlobalMonitoringSettingEnabled=false`) | `appdbreplicaset_controller_test.go` | AC has nil `monitoringVersions` |
| Monitoring toggled off after being on | `appdbreplicaset_controller_test.go` | AC `monitoringVersions` cleared to nil |
| Monitoring toggled back on | `appdbreplicaset_controller_test.go` | AC `monitoringVersions` re-populated |
| OM not yet ready (`podVars.ProjectID == ""`) | `appdbreplicaset_controller_test.go` | AC has no `monitoringVersions` |
| `readExistingPodVars` returns agent API key from secret | `appdbreplicaset_controller_test.go` | When OM is down but projectID configmap + agent API key secret exist, `podVars.AgentAPIKey` is non-empty |
| StatefulSet has exactly one agent container | `appdb_construction_test.go` | No container named `mongodb-agent-monitoring`; one container named `mongodb-agent`; regardless of monitoring flag |
| User pod template with old monitoring container | `appdb_construction_test.go` | Container stripped; warning logged |

### e2e tests

- **`e2e_om_appdb_external_connectivity`** (existing, validated in PoC): single agent container; metrics visible in OM; agent count correct.
- **New: monitoring toggle test** — enable monitoring, verify metrics in OM; set `OPS_MANAGER_MONITOR_APPDB=false`; verify `monitoringVersions` cleared and monitoring stops in OM.

---

## Open questions (from doc, carried forward)

1. Is running automation + monitoring in headless mode in the same process safe for all OM versions? (PoC validated yes for tested versions; no agent changes needed.)
2. `mmsApiKey` embedded in the automation config (a Kubernetes secret) — acceptable security posture? Current assessment: yes, same access control as the agent API key secret itself.
