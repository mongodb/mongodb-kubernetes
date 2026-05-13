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

When `ShouldEnableMonitoring(podVars)` is true, each process entry in the automation config gets a corresponding `monitoringVersions` entry:

```json
"monitoringVersions": [{
  "hostname": "<pod-fqdn>",
  "name":     "<agent-version>",
  "baseUrl":  "<OM base URL>",
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
  "sslRequireValidMMSServerCertificates": "false",
  "sslClientCertificate":                 "<client-cert-path>"
}
```

When `ShouldEnableMonitoring` is false (monitoring globally disabled, or OM not yet ready), `automationConfig.MonitoringVersions` is explicitly set to `nil` to clear any previously written entries.

---

## Code changes

### `env.PodEnvVars`

Add `AgentAPIKey string` field. Populated in:
- `tryConfigureMonitoringInOpsManager` — from `ensureAppDbAgentApiKey` (agent API key, not OM admin key)
- `readExistingPodVars` — read from the agent API key secret (fix PoC bug: was using `cred.PrivateAPIKey`)

### `appdbreplicaset_controller.go`

| Removed | Notes |
|---|---|
| `deployMonitoringAgentAutomationConfig()` | Monitoring AC merged into main AC |
| `getLegacyMonitoringAgentVersion()` | No separate legacy monitoring image |
| `appdbOpts.LegacyMonitoringAgentImage` | Removed from `AppDBStatefulSetOptions` |
| Publishing monitoring AC version configmap | No separate monitoring configmap |
| Separate `monitoring` `agentType` handling in `buildAppDbAutomationConfig` | Single AC now covers both roles |

`buildAppDbAutomationConfig` signature: `podVars *env.PodEnvVars` replaces `acType agentType` as the driver for whether to add monitoring entries.

`addMonitoring` (renamed `configureMonitoring` for clarity): takes `projectID string` and `agentAPIKey string`; always adds them to `additionalParams` regardless of TLS; TLS fields added on top when TLS is enabled. Always clears `MonitoringVersions` when not enabling monitoring.

`deployAutomationConfigAndWaitForAgentsReachGoalState` and its callees receive `podVars *env.PodEnvVars` so monitoring params flow through without global state.

### `construct/appdb_construction.go`

| Removed | Notes |
|---|---|
| `addMonitoringContainer()` (~280 lines) | Entire function deleted |
| `removeContainerByName()` call in `AppDbStatefulSet` | No monitoring container to remove |
| `monitoringAgentContainerName` container | Gone |
| Monitoring AC secret volume + mount | Gone |
| Monitoring AC goal-version configmap + mount | Gone |
| `AgentAPIKeyVolumeName` volume + mount | Gone (API key now in AC `additionalParams`) |
| `tmpSubpathName` | Gone |
| `monitoringAgentHealthStatusFilePathValue` | Gone |
| `ShouldEnableMonitoring` conditional in `AppDbStatefulSet` | Gone |

`AutomationAgentCommand` called with `withAgentAPIKeyExport=false` for AppDB — the bash preamble no longer exports `AGENT_API_KEY` since the monitoring module reads it from the AC.

Sanitisation: any container named `monitoringAgentContainerName` in a user-supplied `podTemplateSpec` is stripped with a warning log, to handle clusters upgrading from the two-container model.

### `construct/appdb_agent_command.go`

`withAgentAPIKeyExport` path still exists for non-AppDB callers; no change needed here — the AppDB call site changes the argument.

---

## `ShouldEnableMonitoring` stays

`ShouldEnableMonitoring(podVars)` — which checks both `GlobalMonitoringSettingEnabled()` and `podVars.ProjectID != ""` — continues to guard:
- Whether `monitoringVersions` are populated in the automation config
- The toggle-off path: when it returns false, `MonitoringVersions` is explicitly cleared

The StatefulSet always has exactly **one** agent container regardless of this flag.

---

## Error handling

| Situation | Behaviour |
|---|---|
| OM not yet ready (first boot) | AC deployed without `monitoringVersions`. Monitoring added on next successful reconcile. Zero pod restarts. |
| OM goes down after monitoring was configured | `readExistingPodVars` returns cached project ID + agent API key. `monitoringVersions` remain in AC. No StatefulSet change. |
| Stale/wrong agent API key in AC | Monitoring module rejects auth; retries on next AC reload. Operator corrects on next reconcile. |
| `GlobalMonitoringSettingEnabled()` toggled off after monitoring was running | `MonitoringVersions` explicitly cleared in AC on next reconcile. Monitoring module stops. No pod restart. |
| `GlobalMonitoringSettingEnabled()` toggled back on | `monitoringVersions` re-populated. No pod restart. |
| User pod template names `mongodb-agent-monitoring` container | Stripped with a warning during merge; not deployed. |

---

## Testing

### Unit tests

| Scenario | Location | What to assert |
|---|---|---|
| Monitoring enabled (non-TLS) | `appdbreplicaset_controller_test.go` | AC has `monitoringVersions` with `mmsGroupId`, `mmsApiKey`; no TLS fields |
| Monitoring enabled (TLS) | `appdbreplicaset_controller_test.go` | AC has full `additionalParams` including TLS fields |
| Monitoring disabled from start (`GlobalMonitoringSettingEnabled=false`) | `appdbreplicaset_controller_test.go` | AC has empty/nil `monitoringVersions` |
| Monitoring toggled off after being on | `appdbreplicaset_controller_test.go` | AC `monitoringVersions` cleared to nil |
| Monitoring toggled back on | `appdbreplicaset_controller_test.go` | AC `monitoringVersions` re-populated |
| OM not yet ready (`podVars.ProjectID == ""`) | `appdbreplicaset_controller_test.go` | AC has no `monitoringVersions` |
| StatefulSet always has one agent container | `appdb_construction_test.go` | No container named `mongodb-agent-monitoring` regardless of monitoring flag |
| User pod template with old monitoring container | `appdb_construction_test.go` | Container stripped; warning logged |

### e2e tests

- **`e2e_om_appdb_external_connectivity`** (existing, already validated in PoC): single agent container; metrics visible in OM; agent count correct.
- **New: monitoring toggle test** — enable monitoring, verify metrics; disable `OPS_MANAGER_MONITOR_APPDB`; verify `monitoringVersions` cleared and monitoring stops.

---

## Open questions (from doc, carried forward)

1. Is running automation + monitoring in headless mode in the same process safe for all OM versions? (PoC validated yes for tested versions; no agent changes needed.)
2. `mmsApiKey` embedded in the automation config (a Kubernetes secret) — acceptable security posture? Current assessment: yes, same access control as the agent API key secret.
