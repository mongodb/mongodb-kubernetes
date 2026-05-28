# MongoDB Enterprise STS vs AppDB STS — Construction Comparison

> Investigated in context of PR #1112 (`SKUNK-209: refactor(appdb): collapse AppDB monitoring into single automation agent`).
> All references are to files under `controllers/operator/construct/`.

---

## 1. Entry Points and Signatures

| | MongoDB STS | AppDB STS |
|---|---|---|
| **Function** | `DatabaseStatefulSet()` in `database_construction.go:333` | `AppDbStatefulSet()` in `appdb_construction.go:377` |
| **Returns** | `appsv1.StatefulSet` | `(appsv1.StatefulSet, error)` |
| **Source type** | `mdbv1.MongoDB` | `om.MongoDBOpsManager` |
| **Options** | `DatabaseStatefulSetOptions` via factory func | `AppDBStatefulSetOptions` struct directly |
| **Scaler** | `opts.Replicas` (int, set in options factory) | `interfaces.MultiClusterReplicaSetScaler` passed explicitly |
| **Update strategy** | Default (not set, falls to `RollingUpdate`) | `updateStrategyType appsv1.StatefulSetUpdateStrategyType` explicit param |
| **`podVars`** | Embedded in `DatabaseStatefulSetOptions.PodVars` | `*env.PodEnvVars` explicit param |

---

## 2. Options Structs

`DatabaseStatefulSetOptions` (`database_construction.go:87`) is much richer than `AppDBStatefulSetOptions` (`appdb_construction.go:53`).

**Fields only in `DatabaseStatefulSetOptions`:**
- `Replicas`, `ServicePort`, `StsType` (Standalone / ReplicaSet / Mongos / Config / Shard / Multi)
- `CurrentAgentAuthMode`, `AgentCertHash`, `InternalClusterHash`
- `AgentConfig *mdbv1.AgentConfig`, `AdditionalMongodConfig`
- `Persistent *bool`
- `Annotations`, `ExtraEnvs`, `StsLabels`
- `MultiClusterMode bool`, `StatefulSetNameOverride`, `HostNameOverrideConfigmapName`
- `AgentDebug bool`, `AgentDebugImage`
- `DatabaseNonStaticImage` (non-static only image, separate from `MongodbImage`)

**Fields only in `AppDBStatefulSetOptions`:**
- `LegacyMonitoringAgentImage` (for the now-deprecated monitoring sidecar)

**Fields in both:**
- `VaultConfig`, `CertHash` / `CertificateHash`, `PrometheusTLSCertHash`
- `InitDatabaseImage` / `InitAppDBImage`, `MongodbImage`, `AgentImage`

---

## 3. Container Layout

### Non-static architecture

| | MongoDB STS | AppDB STS |
|---|---|---|
| **Init containers** | 1: `mongodb-kubernetes-init-database` — copies agent scripts to `/opt/scripts` | 2: `mongod-posthook` + `mongodb-agent-readinessprobe` — copy version-upgrade-hook and readinessprobe binary |
| **Main containers** | 1: `mongodb-agent` — single container runs both agent and mongod via `agent-launcher.sh` | 2: `mongodb-agent` + `mongodb` — separate containers |
| **Sidecar** | None | Optionally: `mongodb-agent-monitoring` (deprecated by PR #1112) |

### Static architecture

| | MongoDB STS | AppDB STS |
|---|---|---|
| **Init containers** | None | None |
| **Container 0** | `mongodb-agent` — runs `/usr/local/bin/agent-launcher-shim.sh` | `mongodb-agent` — runs agent binary directly with full command string |
| **Container 1** | `mongodb` — runs `bash -c "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"` | `mongodb` — runs mongod via the `appdbBuildMongodbCommand` shell script |
| **Container 2** | `mongodb-agent-utilities` — holds binaries, uses `initDatabaseImage` | `mongodb-agent-utilities` — holds binaries, uses `initAppDBImage` |
| **shareProcessNamespace** | `true` | `true` |

The key structural difference: **MongoDB STS uses a single-container design** in non-static mode (agent manages mongod internally via launcher script), while **AppDB always uses two separate containers** (mongod and agent are always independent processes).

---

## 4. Agent Command Construction

These are fundamentally different approaches.

**MongoDB** (`database_construction.go:863–888`, `722–727`):
Startup parameters are encoded as a comma-separated `AGENT_FLAGS` env var:
```
AGENT_FLAGS=-logFile=/var/log/...,
```
The agent launcher script reads `AGENT_FLAGS` and passes them. Env vars `MDB_LOG_FILE_*` are set separately for log path configuration. The agent binary is not invoked directly by the operator — the launcher script calls it.

**AppDB** (`appdb_agent_command.go:48–65`, `appdb_construction.go:400–404`):
Full bash command string is built inline by the operator and placed into the container `command`:
```bash
/bin/bash -c "...preamble... agent/mongodb-agent -healthCheckFilePath=... -cluster=... -skipMongoStart -noDaemonize ... <startup params>"
```
Startup parameters are appended directly to the command string via `ToCommandLineArgs()`. No launcher script intermediary.

**Implication**: MongoDB can change `AGENT_FLAGS` via a ConfigMap/Env update without a pod restart; AppDB requires a pod restart for command changes.

---

## 5. Mongod Container Command

**MongoDB** (non-static): mongod is managed entirely by the agent via `agent-launcher.sh`. The operator never writes a mongod command.

**MongoDB** (static, `database_construction.go:733–738`):
```bash
bash -c "tail -F -n0 ${MDB_LOG_FILE_MONGODB} mongodb_marker"
```
mongod is launched by the agent process running in container 0 via the shared PID namespace.

**AppDB** (`appdb_statefulset.go:181–211`): The operator builds the entire mongod invocation:
```bash
# optional SIGTERM handler (static only)
# run version-upgrade-hook if present
# wait for config file and keyfile to be created by the agent
echo "Sleeping for 15s..."
sleep 15
exec mongod -f <datadir>/automation-mongod.conf
```
The 15-second sleep and wait loop are AppDB-specific with no equivalent in the MongoDB STS path.

---

## 6. Volumes

### Agent-specific volumes

| Volume | MongoDB STS | AppDB STS |
|---|---|---|
| `healthstatus` (emptyDir) | Via `GetNonPersistentAgentVolumeMounts()` — MMS home dirs | Explicit: mounted at `/var/log/mongodb-mms-automation/healthstatus` |
| `keyfile` / auth (emptyDir) | Via MMS volumes | Explicit: `keyFileVolume` at `/var/lib/mongodb-mms-automation/authentication` |
| `tmp` (emptyDir) | Via `GetNonPersistentAgentVolumeMounts()` | Explicit: mounted at `/tmp` |
| `agent-api-key` (Secret) | Always unconditional (`getVolumesAndVolumeMounts:683`) | Conditional: only when `ShouldEnableMonitoring()` is true |
| `automation-config` (Secret) | N/A — agent uses OM connection | Conditional: only when `NeedsAutomationConfigVolume()` |
| `automation-config-goal-version` (ConfigMap) | N/A | Always present |
| `database-scripts` (emptyDir) | Always — for init/launcher scripts | Named `agent-scripts`; conditional on architecture |
| `hooks` (emptyDir) | N/A | Present for version-upgrade-hook |
| `HostNameOverrideConfigmap` | Supported (`opts.HostNameOverrideConfigmapName`) | Not supported |

### TLS volumes

**MongoDB**: `tlsVolumeSource` and `caVolumeSource` implement the `MongoDBVolumeSource` interface, collected via `getAllMongoDBVolumeSources()`. CA configmap at `util.TLSCaMountPath`, cert secret at `util.TLSCertMountPath`.

**AppDB**: `getTLSVolumesAndVolumeMounts()` builds volumes directly. CA configmap at `util.ConfigMapVolumeCAMountPath`, cert secret at `util.SecretVolumeMountPath + "/certs"`. Additionally mounts `agent-api-key` alongside TLS volumes (`appdb_construction.go:238–241`).

The mount paths for TLS material differ between the two paths.

---

## 7. Persistence

**MongoDB** (`database_construction.go:556–579`): `buildPersistentVolumeClaimsFuncs()` handles single vs multiple persistence via `createClaimsAndMountsSingleModeFunc` / `createClaimsAndMountsMultiModeFunc`. Supports `*opts.Persistent == false` for non-persistent (emptyDir) deployments.

**AppDB** (`appdb_construction.go:301–358`): `customPersistenceConfig()` uses `appdbDataPvc()` and `appdbLogsPvc()` helpers. Always persistent — no non-persistent mode. In single-volume mode it creates journal + logs as subpaths of the data volume. In multi-volume mode it explicitly adds a separate journal PVC.

The AppDB code duplicates journal/logs subpath logic inside `addMonitoringContainer()` (`appdb_construction.go:677–685`) to mirror `customPersistenceConfig()` — a known drift called out in comments.

---

## 8. Labels

**MongoDB** (`database_construction.go:413–418`, `467–470`):
- Pod labels: `app: <serviceName>`, `pod-anti-affinity: <name>`
- STS labels: `app: <serviceName>` only

**AppDB** (`appdb_construction.go:90–108`):
- Pod labels: `app: <HeadlessServiceSelectorAppLabel>`, `pod-anti-affinity: <NameForCluster>`
- STS labels: inherits from `opsManager.Labels` + `GetOwnerLabels()`

AppDB propagates the parent OM's labels onto the STS; MongoDB does not.

---

## 9. Probes

| | MongoDB agent container | AppDB agent container | AppDB mongod container |
|---|---|---|---|
| Liveness | `DatabaseLivenessProbe` — `/opt/scripts/probe.sh`, threshold 6 | None | None |
| Readiness | `DatabaseReadinessProbe` — `/opt/scripts/readinessprobe`, threshold 4, period 5s | `appdbDefaultReadiness` — `/opt/scripts/readinessprobe`, threshold **40**, period default | None |
| Startup | `DatabaseStartupProbe` — `/opt/scripts/probe.sh`, threshold 10 | None | None |

AppDB's much higher readiness failure threshold (40 vs 4) reflects that AppDB pods take longer to start because mongod is a separate process that must be waited on by the agent.

---

## 10. Service Account

| | MongoDB STS | AppDB STS |
|---|---|---|
| Default | `util.MongoDBServiceAccount` | `"mongodb-kubernetes-appdb"` (const in `appdb_construction.go:36`) |
| CR override | Yes — from `podSpec.PodTemplateWrapper.PodTemplate.Spec.ServiceAccountName` | No — always `mongodb-kubernetes-appdb` |

---

## 11. Env Vars

**MongoDB** (`database_construction.go:990–1037`): `databaseEnvVars()` sets:
- `MDB_LOG_LEVEL`, `MDB_BASE_URL`, `MDB_ORG_ID`, `MDB_USER`, `MDB_CLUSTER_MEMBER`, `SSL_REQUIRE_VALID_MMS_CERTIFICATES`
- `AGENT_FLAGS` (startup parameters)
- `MDB_LOG_FILE_*` (log paths per component)
- `MDB_STATIC_CONTAINERS_ARCHITECTURE` (if static)
- `READINESS_PROBE_*` (from `AgentConfig.ReadinessProbe.EnvironmentVariables`)

**AppDB** (`appdb_construction.go:704–725`, `appdb_statefulset.go:40–63`): `appdbContainerEnv()` sets:
- `POD_NAMESPACE`, `AUTOMATION_CONFIG_MAP`, `HEADLESS_AGENT=true`, `CLUSTER_DOMAIN`
- `AGENT_STATUS_FILEPATH`
- `READINESS_PROBE_*`, `MDB_WITH_AGENT_FILE_LOGGING` (read from operator env, not CR)

MongoDB exposes OM connection details (base URL, project ID, user) as env vars; AppDB does not — instead the agent reads its config from the automation config secret/configmap.

---

## 12. Affinity and Scheduling

**MongoDB** (`database_construction.go:528–529`): `WithAffinity(podAffinity, PodAntiAffinityLabelKey, 100)` + `WithTopologyKey(opts.PodSpec.GetTopologyKeyOrDefault(), 0)` — both explicitly applied.

**AppDB**: No affinity or topology key call in `AppDbStatefulSet`. Affinity must come from the user's `podSpec.PodTemplateWrapper.PodTemplate` override.

---

## 13. Termination Grace Period

**MongoDB**: `WithTerminationGracePeriodSeconds(util.DefaultPodTerminationPeriodSeconds)` always set.

**AppDB**: Not set by the construction function — relies on the Kubernetes default (30s) unless the user overrides via PodSpec. The AppDB mongod shell script in static mode does implement its own SIGTERM handler referencing `DefaultPodTerminationPeriodSeconds`, but the STS-level field is never set.

---

## 14. Image Pull Policy

**MongoDB**: reads `util.AutomationAgentImagePullPolicy` from env via `env.ReadOrPanic()`.

**AppDB**: hardcodes `corev1.PullAlways` in `appdbMongodbAgentContainer()` and `appdbMongodbAgentUtilitiesContainer()`.

---

## 15. Vault Handling

**MongoDB** (`database_construction.go:376–408`): `buildVaultDatabaseSecretsToInject()` builds a `vault.DatabaseSecretsToInject` covering agent certs, internal cluster auth, Prometheus, and member certs in one pass, applied as pod template annotations.

**AppDB** (`appdb_construction.go:262–299`): `vaultModification()` builds `vault.AppDBSecretsToInject` separately. When Vault is enabled it sets AC secret path + agent type annotation. When Vault is disabled it creates the `agent-api-key` Secret volume (conditionally on monitoring being enabled).

The two vault paths use different annotation builders and different secret injection structs with no shared abstraction.

---

## 16. Spec Override / PodSpec Merge

**MongoDB** (`database_construction.go:349–353`):
```go
dbSts.Spec = merge.StatefulSetSpecs(dbSts.Spec, *stsOptions.StatefulSetSpecOverride)
```
Applied in `DatabaseStatefulSet()` after construction — one pass.

**AppDB** (`appdb_construction.go:527–538`):
```go
sts.Spec = merge.StatefulSetSpecs(sts.Spec, appsv1.StatefulSetSpec{Template: *podSpec})
```
Plus a second per-cluster merge for multi-cluster `clusterSpecList[i].StatefulSetConfiguration`. AppDB does two merge passes; MongoDB does one.

---

## 17. Multi-cluster

**MongoDB**: `StatefulSetNameOverride` + `HostNameOverrideConfigmapName` plumbed through options. The configmap is mounted as a volume to allow the agent to discover the correct hostname.

**AppDB**: `scaler.MemberClusterName()` / `MemberClusterNum()` drive name and service name. `overrideLocalHostFlag()` appends `-overrideLocalHost=...` directly to the agent command string. Per-cluster `StatefulSetConfiguration` override is merged after initial construction.

---

## 18. `overrideLocalHost` (AppDB-only)

`appdb_construction.go:403–404, 730–737`: AppDB appends `-overrideLocalHost=$(hostname).{externalDomain}` or `$(hostname)-svc.${POD_NAMESPACE}.svc.{clusterDomain}` to the agent command. No equivalent in the MongoDB STS path — hostname override there goes through a configmap volume.

---

## How Close Are the Resulting StatefulSets?

In practice the STS output is **structurally similar but not identical**:

- Both produce an STS with `mongodb-agent` + `mongodb` containers in static mode.
- Both use `pod-anti-affinity` labels and owner references.
- Both mount TLS certs and agent API key secrets.
- Both handle Vault via pod annotations.

Key output-level divergences:

1. AppDB always has two containers (agent + mongod); MongoDB non-static has one.
2. The mongod `command` is a wait-loop bash script in AppDB; a tail no-op in MongoDB static.
3. AppDB has keyfile + healthstatus + acVersion volumes with no equivalent in MongoDB.
4. MongoDB has MMS home dir volumes with no equivalent in AppDB.
5. Mount paths for TLS material differ.
6. AppDB does not set affinity or termination grace period at STS level.
7. Agent startup params: env var (`AGENT_FLAGS`) in MongoDB, command string in AppDB.

---

## What It Would Take to Combine the Logic

A significant refactor. Blockers in order of difficulty:

**1. Source type incompatibility (hard)**
`mdbv1.MongoDB` and `om.AppDBSpec` are unrelated types. The existing `databaseStatefulSetSource` interface (`database_construction.go:142–151`) only covers `GetName`, `GetNamespace`, `GetSecurity`, `GetPrometheus`, `GetAnnotations`. AppDB additionally needs `ClusterDomain`, `IsMultiCluster`, `NeedsAutomationConfigVolume`, `AutomationConfigSecretName`, etc. Either extend the interface significantly or accept two concrete implementations.

**2. Agent command construction (hard)**
MongoDB uses env-var-based `AGENT_FLAGS` via launcher scripts. AppDB builds a full bash command string. These are fundamentally different deployment models. Unifying requires picking one model and migrating the other — the static path is closer (both run the agent binary directly), but the non-static paths are very different.

**3. Mongod startup (medium)**
AppDB's mongod shell script (wait loop, 15s sleep, SIGTERM handler) cannot be used for MongoDB STS where the agent manages mongod internally. The approaches must stay separate unless MongoDB also moves universally to the two-container model.

**4. Volume sets (medium)**
AppDB-specific: `keyfile` emptyDir, `healthstatus` emptyDir, `acVersion` configmap, `agent-scripts`, `hooks`. MongoDB-specific: `database-scripts`, MMS home/data dirs. Harmonization requires careful migration to avoid data loss on in-place upgrades.

**5. Persistence abstraction (medium)**
`customPersistenceConfig` and `buildPersistentVolumeClaimsFuncs` have overlapping but non-identical logic. AppDB has no non-persistent mode; MongoDB does. The journal subpath duplication in `addMonitoringContainer` is a symptom of this drift.

**6. Options structs (easy)**
`AppDBStatefulSetOptions` is a strict subset of `DatabaseStatefulSetOptions`. Merging is trivial — add the AppDB-only fields to the common struct and deprecate the smaller one.

**7. Labels / SA / probes (easy)**
Different values for the same fields. Can be parameterized through the options struct without structural change.

The most realistic path to unification is: **unify the static architecture path first** (it is already closer), then progressively migrate AppDB non-static to align with the MongoDB model. The two-container mongod design in AppDB is the deepest architectural difference and the hardest to bridge.
