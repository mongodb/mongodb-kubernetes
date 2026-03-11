# kubectl mongodb migrate

Generates a MongoDB Custom Resource (CR) from an Ops Manager Automation Config,
enabling migration of VM-managed replica sets to the Kubernetes operator.

The generated CR must match the existing Automation Config so that when the
operator pushes its updated AC to Ops Manager, the running deployment is not
changed.

## Category Reference

The code is organized by numbered categories that map Automation Config (AC)
sections to CR fields. Each category appears as a section header comment
(e.g. `// 7. ldap - LDAP Configuration`) across the source files.


| #   | Category                          | Source Files                                     |
| --- | --------------------------------- | ------------------------------------------------ |
| 1   | auth - Authentication             | `security.go`, `validation.go`, `generator.go`   |
| 2   | tls/ssl - TLS Configuration       | `security.go`, `validation.go`                   |
| 3   | Monitoring Agent Config           | `validation.go`                                  |
| 4   | Backup Agent Config               | `validation.go`                                  |
| 5   | roles - Custom Roles              | `security.go`, `generator.go`                    |
| 6   | prometheus                        | `security.go`, `generator.go`                    |
| 7   | ldap - LDAP Configuration         | `security.go`, `validation.go`                   |
| 8   | oidc - OIDC Configuration         | `security.go`                                    |
| 9   | Project-Level Options             | `validation.go`                                  |
| 10  | **MongoDB Process Configuration** |                                                  |
| 10A | Core Process Identity             | `validation.go`, `replicaset.go`                 |
| 10B | Version & Compatibility           | `validation.go`, `replicaset.go`, `generator.go` |
| 10C | args2_6 / additionalMongodConfig  | `security.go`, `validation.go`, `generator.go`   |
| 10D | Log Rotation                      | `security.go`, `generator.go`                    |
| 11  | **Replica Set Configuration**     |                                                  |
| 11A | Replica Set Root                  | `validation.go`, `replicaset.go`, `generator.go` |
| 11B | Replica Set Members               | `validation.go`, `replicaset.go`, `generator.go` |


## Migration Impact Legend


| Impact         | Meaning                                                                                                                     |
| -------------- | --------------------------------------------------------------------------------------------------------------------------- |
| **Must match** | Automatically extracted into the CR. Operator writes this value back to the AC, so it must match to avoid changing the deployment. |
| **Blocker**    | Migration cannot proceed. Existing value is incompatible with the operator; must be resolved before migration.               |
| **Preserved**  | No action needed. Operator preserves the existing value, or the field has no effect on the deployment.                       |
| **Managed**    | Operator takes ownership of this field. Existing value may be replaced after migration; this is expected for K8s operation.  |


## Field Mapping: Automation Config → CR

### 1. auth - Authentication Configuration

| AC Field                        | CR Field                                          | Impact         | Notes                                                    |
| ------------------------------- | ------------------------------------------------- | -------------- | -------------------------------------------------------- |
| `auth.deploymentAuthMechanisms` | `spec.security.authentication.modes`              | **Must match** | Mismatch changes auth config                             |
| `auth.autoAuthMechanisms`       | `spec.security.authentication.modes`              | **Must match** | Merged with `deploymentAuthMechanisms`, deduplicated     |
| `auth.autoAuthMechanism`        | —                                                 | Preserved      | Read-only; derived from `autoAuthMechanisms`             |
| `auth.authoritativeSet`         | `spec.security.authentication.ignoreUnknownUsers` | **Must match** | Value is inverted in CR                                  |
| `auth.disabled`                 | `spec.security.authentication.enabled`            | **Must match** | Value is inverted in CR                                  |
| `auth.autoUser`                 | `spec.security.authentication.agents.automationUserName` | Extracted | Mapped to CR; defaults to `mms-automation-agent`   |
| `auth.autoPwd`                  | —                                                 | Preserved      | Reads existing AC value; regenerated only if empty       |
| `auth.newAutoPwd`               | —                                                 | Preserved      | Never modified; round-trips via `omitempty` merge        |
| `auth.key`                      | —                                                 | Preserved      | Only regenerated if empty or placeholder                 |
| `auth.keyFile`                  | —                                                 | **Blocker**    | Hardcodes container path; error if differs               |
| `auth.keyFileWindows`           | —                                                 | **Blocker**    | Hardcodes Windows path; error if differs                 |
| `auth.autoLdapGroupDN`          | —                                                 | Preserved      | `omitempty` preserves original; not extracted             |
| `auth.usersWanted`              | `MongoDBUser` CRs                                 | **Must match** | Separate CR per user; missing removed if authoritative   |


### 2. tls/ssl - TLS Configuration


| AC Field                             | CR Field                                   | Impact         | Notes                                                             |
| ------------------------------------ | ------------------------------------------ | -------------- | ----------------------------------------------------------------- |
| `args2_6.net.tls.mode`               | `spec.security.tls.enabled`                | **Must match** | Mismatch toggles TLS on/off                                       |
| `args2_6.net.tls.mode`               | `spec.additionalMongodConfig.net.tls.mode` | **Must match** | Only if non-default; preserves `allowTLS`/`preferTLS`             |
| `args2_6.net.tls.certificateKeyFile` | —                                          | **Blocker**    | Operator hardcodes container path; error if AC path differs       |
| `args2_6.net.tls.PEMKeyFile`         | —                                          | **Blocker**    | Legacy equivalent of `certificateKeyFile`                         |
| `args2_6.net.ssl.certificateKeyFile` | —                                          | **Blocker**    | Legacy `net.ssl` equivalent; same validation                      |
| `args2_6.net.ssl.PEMKeyFile`         | —                                          | **Blocker**    | Legacy `net.ssl` equivalent; same validation                      |
| `args2_6.net.tls.clusterFile`        | —                                          | **Blocker**    | Operator hardcodes cluster cert path; error if AC path differs    |
| `args2_6.net.ssl.clusterFile`        | —                                          | **Blocker**    | Legacy `net.ssl` equivalent of `tls.clusterFile`                  |
| `tls.clientCertificateMode`          | —                                          | Managed        | Overwrites: `OPTIONAL` for non-X509, `REQUIRE` for X509-only     |
| `tls.autoPEMKeyFilePath`             | —                                          | **Blocker**    | Operator manages this path; error if set in AC                    |
| `tls.CAFilePath`                     | —                                          | **Blocker**    | Must match operator default mount path                            |


### 3. Monitoring Agent Config

**`monitoringVersions`** is a per-host array in the AC. The operator adds/removes entries
automatically based on the processes in the deployment. These are not exposed in the CR.

**`monitoringAgentConfig`** is a separate OM API endpoint
(`GET /automationConfig/monitoringAgentConfig`) with deployment-level agent settings.
The migration tool reads `logRotate` from this endpoint directly instead of from the
per-host `monitoringVersions` array, since the operator writes back to this same endpoint.


| AC Field                                | CR Field                                    | Impact      | Notes                                              |
| --------------------------------------- | ------------------------------------------- | ----------- | -------------------------------------------------- |
| `monitoringVersions[]`                  | —                                           | Managed     | Operator adds/removes per-host entries             |
| `monitoringVersions[].hostname`         | —                                           | Managed     | Matched to processes by hostname                   |
| `monitoringVersions[].name`             | —                                           | Managed     | Operator uses default agent version                |
| `monitoringVersions[].additionalParams` | —                                           | Managed     | TLS params set from CR TLS config                  |
| `monitoringAgentConfig.username`        | —                                           | Managed     | Set based on auth mode (x509 / LDAP)               |
| `monitoringAgentConfig.password`        | —                                           | Managed     | Set for LDAP auth; managed via K8s secret          |
| `monitoringAgentConfig.sslPEMKeyFile`   | —                                           | Managed     | Set when x509 is enabled                           |
| `monitoringAgentConfig.ldapGroupDN`     | —                                           | Managed     | Set when LDAP is enabled                           |
| `monitoringAgentConfig.logPath`         | —                                           | **Blocker** | Operator hardcodes path; error if AC path differs  |
| `monitoringAgentConfig.logRotate`       | `spec.agent.monitoringAgent.logRotate`      | **Must match** | Read from endpoint; operator writes same endpoint |


### 4. Backup Agent Config

**`backupVersions`** is a per-host array in the AC. The operator adds/removes entries
automatically based on the processes in the deployment. These are not exposed in the CR.

**`backupAgentConfig`** is a separate OM API endpoint
(`GET /automationConfig/backupAgentConfig`) with deployment-level agent settings.


| AC Field                          | CR Field | Impact      | Notes                                              |
| --------------------------------- | -------- | ----------- | -------------------------------------------------- |
| `backupVersions[]`                | —        | Managed     | Operator adds/removes per-host entries             |
| `backupVersions[].hostname`       | —        | Managed     | Matched to processes by hostname                   |
| `backupVersions[].name`           | —        | Managed     | Operator uses default agent version                |
| `backupAgentConfig.username`      | —        | Managed     | Set based on auth mode                             |
| `backupAgentConfig.password`      | —        | Managed     | Set for LDAP auth; managed via K8s secret          |
| `backupAgentConfig.sslPEMKeyFile` | —        | Managed     | Set when x509 is enabled                           |
| `backupAgentConfig.ldapGroupDN`   | —        | Managed     | Set when LDAP is enabled                           |
| `backupAgentConfig.logPath`       | —        | **Blocker** | Operator hardcodes path; error if AC path differs  |
| `backupAgentConfig.logRotate`     | —        | Managed     | Set from CR agent config if provided               |


### 5. roles - Custom Roles


| AC Field | CR Field              | Impact         | Notes                                                   |
| -------- | --------------------- | -------------- | ------------------------------------------------------- |
| `roles`  | `spec.security.roles` | **Must match** | CR roles overwrite matching; removed roles are deleted; externally-added roles preserved |


### 6. prometheus


| AC Field                   | CR Field                            | Impact         | Notes                                               |
| -------------------------- | ----------------------------------- | -------------- | --------------------------------------------------- |
| `prometheus.enabled`       | `spec.prometheus` (presence)        | **Must match** | If omitted from CR, existing config is preserved     |
| `prometheus.username`      | `spec.prometheus.username`          | **Must match** | —                                                    |
| `prometheus.listenAddress` | `spec.prometheus.port`              | **Must match** | Port parsed from `host:port`                         |
| `prometheus.metricsPath`   | `spec.prometheus.metricsPath`       | **Must match** | Only extracted if non-default (`/metrics`)           |
| `prometheus.scheme`        | `spec.prometheus.tlsSecretRef`      | **Must match** | `https` → TLS secret ref (`prometheus-tls`)          |
| `prometheus.password`      | `spec.prometheus.passwordSecretRef` | Managed        | References `prometheus-password` secret              |


### 7. ldap - LDAP Configuration

| AC Field                             | CR Field                                                          | Impact         | Notes                                                  |
| ------------------------------------ | ----------------------------------------------------------------- | -------------- | ------------------------------------------------------ |
| `ldap.servers`                       | `spec.security.authentication.ldap.servers`                       | **Must match** | Operator overwrites LDAP config in AC from CR          |
| `ldap.bindQueryUser`                 | `spec.security.authentication.ldap.bindQueryUser`                 | **Must match** | —                                                      |
| `ldap.bindQueryPassword`             | `spec.security.authentication.ldap.bindQueryPasswordSecretRef`    | Managed        | References `ldap-bind-query-password` secret           |
| `ldap.authzQueryTemplate`            | `spec.security.authentication.ldap.authzQueryTemplate`            | **Must match** | —                                                      |
| `ldap.userToDNMapping`               | `spec.security.authentication.ldap.userToDNMapping`               | **Must match** | —                                                      |
| `ldap.timeoutMS`                     | `spec.security.authentication.ldap.timeoutMS`                     | **Must match** | —                                                      |
| `ldap.userCacheInvalidationInterval` | `spec.security.authentication.ldap.userCacheInvalidationInterval` | **Must match** | —                                                      |
| `ldap.transportSecurity`             | `spec.security.authentication.ldap.transportSecurity`             | **Must match** | —                                                      |
| `ldap.validateLDAPServerConfig`      | `spec.security.authentication.ldap.validateLDAPServerConfig`      | **Must match** | —                                                      |
| `ldap.CAFileContents`                | `spec.security.authentication.ldap.caConfigMapRef`                | Managed        | References `ldap-ca` ConfigMap with key `ca.pem`       |
| `ldap.bindMethod`                    | —                                                                 | Managed        | Operator hardcodes `simple`; may change existing value |
| `ldap.bindSaslMechanisms`            | —                                                                 | Managed        | Only relevant when `bindMethod` is `sasl`              |


### 8. oidc - OIDC Configuration

| AC Field                                      | CR Field                                                                 | Impact         | Notes                                           |
| --------------------------------------------- | ------------------------------------------------------------------------ | -------------- | ----------------------------------------------- |
| `oidcProviderConfigs[].authNamePrefix`        | `spec.security.authentication.oidcProviderConfigs[].configurationName`   | **Must match** | Operator overwrites OIDC config in AC from CR   |
| `oidcProviderConfigs[].issuerUri`             | `spec.security.authentication.oidcProviderConfigs[].issuerURI`           | **Must match** | —                                               |
| `oidcProviderConfigs[].audience`              | `spec.security.authentication.oidcProviderConfigs[].audience`            | **Must match** | —                                               |
| `oidcProviderConfigs[].clientId`              | `spec.security.authentication.oidcProviderConfigs[].clientId`            | **Must match** | —                                               |
| `oidcProviderConfigs[].userClaim`             | `spec.security.authentication.oidcProviderConfigs[].userClaim`           | **Must match** | —                                               |
| `oidcProviderConfigs[].groupsClaim`           | `spec.security.authentication.oidcProviderConfigs[].groupsClaim`         | **Must match** | —                                               |
| `oidcProviderConfigs[].requestedScopes`       | `spec.security.authentication.oidcProviderConfigs[].requestedScopes`     | **Must match** | —                                               |
| `oidcProviderConfigs[].supportsHumanFlows`    | `spec.security.authentication.oidcProviderConfigs[].authorizationMethod` | **Must match** | `true` → Workforce, `false` → Workload          |
| `oidcProviderConfigs[].useAuthorizationClaim` | `spec.security.authentication.oidcProviderConfigs[].authorizationType`   | **Must match** | `true` → GroupMembership, `false` → UserID      |


### 9. Project-Level Options


| AC Field               | CR Field | Impact      | Notes                                                   |
| ---------------------- | -------- | ----------- | ------------------------------------------------------- |
| `options.downloadBase` | —        | **Blocker** | Operator hardcodes PVC mount path; error if path differs|


### 10. MongoDB Process Configuration

#### 10A. Core Process Identity


| AC Field                  | CR Field                 | Impact         | Notes                                                  |
| ------------------------- | ------------------------ | -------------- | ------------------------------------------------------ |
| `processes[].name`        | `spec.externalMembers[]` | **Must match** | Mismatch causes member mismapping                      |
| `processes[].hostname`    | —                        | Preserved      | External members keep existing hostname                |
| `processes[].processType` | —                        | **Blocker**    | Must be `mongod`; operator only supports `mongod`      |
| `processes[].disabled`    | —                        | Managed        | Warning issued; included in CR, may be re-enabled      |
| `processes[].alias`       | —                        | Preserved      | Only used by operator for sharded clusters              |


#### 10B. Version & Compatibility


| AC Field                                  | CR Field                           | Impact         | Notes                                                |
| ----------------------------------------- | ---------------------------------- | -------------- | ---------------------------------------------------- |
| `processes[].version`                     | `spec.version`                     | **Must match** | Mismatch triggers version change/rolling upgrade     |
| `processes[].featureCompatibilityVersion` | `spec.featureCompatibilityVersion` | **Must match** | Mismatch triggers FCV change                         |
| `processes[].authSchemaVersion`           | —                                  | **Blocker**    | Operator hardcodes value; error if AC value differs  |


#### 10C. args2_6 - MongoDB Configuration (additionalMongodConfig)

| AC Field                                                      | CR Field                                                                          | Impact         | Notes                              |
| ------------------------------------------------------------- | --------------------------------------------------------------------------------- | -------------- | ---------------------------------- |
| `args2_6.net.port`                                            | `spec.additionalMongodConfig.net.port`                                            | **Must match** | Mismatch changes listen port       |
| `args2_6.net.compression.compressors`                         | `spec.additionalMongodConfig.net.compression.compressors`                         | **Must match** | Omitting removes from AC           |
| `args2_6.net.maxIncomingConnections`                          | `spec.additionalMongodConfig.net.maxIncomingConnections`                          | **Must match** | Omitting removes from AC           |
| `args2_6.storage.engine`                                      | `spec.additionalMongodConfig.storage.engine`                                      | **Must match** | Only if non-default (`wiredTiger`) |
| `args2_6.storage.directoryPerDB`                              | `spec.additionalMongodConfig.storage.directoryPerDB`                              | **Must match** | Omitting removes from AC           |
| `args2_6.storage.journal.enabled`                             | `spec.additionalMongodConfig.storage.journal.enabled`                             | **Must match** | Omitting removes from AC           |
| `args2_6.storage.wiredTiger.engineConfig.cacheSizeGB`         | `spec.additionalMongodConfig.storage.wiredTiger.engineConfig.cacheSizeGB`         | **Must match** | Per-member setting                 |
| `args2_6.storage.wiredTiger.engineConfig.journalCompressor`   | `spec.additionalMongodConfig.storage.wiredTiger.engineConfig.journalCompressor`   | **Must match** | Omitting removes from AC           |
| `args2_6.storage.wiredTiger.collectionConfig.blockCompressor` | `spec.additionalMongodConfig.storage.wiredTiger.collectionConfig.blockCompressor` | **Must match** | Omitting removes from AC           |
| `args2_6.replication.oplogSizeMB`                             | `spec.additionalMongodConfig.replication.oplogSizeMB`                             | **Must match** | Omitting removes from AC           |
| `args2_6.setParameter.*`                                      | `spec.additionalMongodConfig.setParameter.*`                                      | **Must match** | Full pass-through; omitting removes all |
| `args2_6.auditLog.*`                                          | `spec.additionalMongodConfig.auditLog.*`                                          | **Must match** | Full pass-through; omitting removes config |
| `args2_6.operationProfiling.*`                                | `spec.additionalMongodConfig.operationProfiling.*`                                | **Must match** | Full pass-through; omitting removes config |
| `args2_6.security.clusterAuthMode`                            | `spec.security.authentication.internalCluster`                                    | **Must match** | Only `x509` supported              |
| `args2_6.storage.dbPath`                                      | —                                                                                 | Managed        | Changes when member transitions to K8s |
| `args2_6.replication.replSetName`                             | —                                                                                 | Preserved      | Derived from `metadata.name`       |
| `args2_6.sharding.clusterRole`                                | —                                                                                 | **Blocker**    | Any `clusterRole` blocks migration |


#### 10D. Log Rotation

On VMs, `processes[]` and `monitoringVersions[]` are per-host arrays where each
mongod or monitoring agent can be configured independently. On Kubernetes, the
operator exposes a single set of configuration that applies uniformly to all
processes. The migration tool reads log rotation from the OM deployment-level
API endpoints (`systemLogRotateConfig`, `auditLogRotateConfig`) instead of
per-process fields, since these endpoints return the uniform values that the
operator writes back. `systemLog` is still extracted from per-process `args2_6`
with intersection (only fields identical across all members are kept).

If omitted from CR, existing config is preserved.

| AC Field (endpoint)                                     | CR Field                                                      | Impact         | Notes                                         |
| ------------------------------------------------------- | ------------------------------------------------------------- | -------------- | --------------------------------------------- |
| `systemLogRotateConfig.sizeThresholdMB`                 | `spec.agent.mongod.logRotate.sizeThresholdMB`                 | **Must match** | Read from `GET .../systemLogRotateConfig`      |
| `systemLogRotateConfig.timeThresholdHrs`                | `spec.agent.mongod.logRotate.timeThresholdHrs`                | **Must match** | —                                             |
| `systemLogRotateConfig.numUncompressed`                 | `spec.agent.mongod.logRotate.numUncompressed`                 | **Must match** | —                                             |
| `systemLogRotateConfig.numTotal`                        | `spec.agent.mongod.logRotate.numTotal`                        | **Must match** | —                                             |
| `systemLogRotateConfig.percentOfDiskspace`              | `spec.agent.mongod.logRotate.percentOfDiskspace`              | **Must match** | —                                             |
| `systemLogRotateConfig.includeAuditLogsWithMongoDBLogs` | `spec.agent.mongod.logRotate.includeAuditLogsWithMongoDBLogs` | **Must match** | —                                             |
| `auditLogRotateConfig.*`                                | `spec.agent.mongod.auditLogRotate.*`                          | **Must match** | Read from `GET .../auditLogRotateConfig`       |
| `args2_6.systemLog.destination`                         | `spec.agent.mongod.systemLog.destination`                     | **Must match** | Per-process; intersected across all members   |
| `args2_6.systemLog.path`                                | `spec.agent.mongod.systemLog.path`                            | **Must match** | —                                             |
| `args2_6.systemLog.logAppend`                           | `spec.agent.mongod.systemLog.logAppend`                       | **Must match** | —                                             |


### 11. Replica Set Configuration

#### 11A. Replica Set Root


| AC Field                        | CR Field                                        | Impact         | Notes                                                |
| ------------------------------- | ----------------------------------------------- | -------------- | ---------------------------------------------------- |
| `replicaSets[]._id`             | `metadata.name` / `spec.replicaSetNameOverride` | **Must match** | Mismatch creates a new replica set                   |
| `replicaSets[].members` (count) | `spec.members`                                  | **Must match** | Mismatch triggers scale up/down                      |
| `replicaSets[].protocolVersion` | —                                               | **Blocker**    | Must be `"1"`; operator hardcodes protocol version 1 |
| `replicaSets[].settings`        | —                                               | Preserved      | Preserved by operator `mergeFrom` logic              |
| `replicaSets[].force`           | —                                               | Preserved      | Recovery-only field; not relevant for migration       |


#### 11B. Replica Set Members


| AC Field                | CR Field                       | Impact         | Notes                                                          |
| ----------------------- | ------------------------------ | -------------- | -------------------------------------------------------------- |
| `members[]._id`         | —                              | Preserved      | External members keep IDs via `mergeFrom`                      |
| `members[].host`        | `spec.externalMembers[]`       | **Must match** | Mismatch causes member mismapping                              |
| `members[].arbiterOnly` | —                              | Preserved      | Used internally for member mapping                             |
| `members[].votes`       | `spec.memberConfig[].votes`    | **Must match** | Set to 0 for draining; external retains via `mergeFrom`        |
| `members[].priority`    | `spec.memberConfig[].priority` | **Must match** | Set to 0 for draining; external retains via `mergeFrom`        |
| `members[].tags`        | `spec.memberConfig[].tags`     | **Must match** | Omitting clears tags                                           |
| `members[].horizons`    | —                              | Managed        | Overwritten for K8s members; external preserved via `mergeFrom`|
| `members[].slaveDelay`  | —                              | Managed        | Lost when member transitions to K8s (not in CRD)              |
| `members[].hidden`      | —                              | Managed        | Lost when member transitions to K8s (not in CRD)              |


## Blocker Summary

All fields that block migration if their existing AC value is incompatible with the operator.

| AC Field                             | Expected Value                                                     | Constant                                    |
| ------------------------------------ | ------------------------------------------------------------------ | ------------------------------------------- |
| `auth.keyFile`                       | `/var/lib/mongodb-mms-automation/keyfile`                          | `util.AutomationAgentKeyFilePathInContainer`|
| `auth.keyFileWindows`                | `%SystemDrive%\MMSAutomation\versions\keyfile`                     | `util.AutomationAgentWindowsKeyFilePath`    |
| `args2_6.net.tls.certificateKeyFile` | `/mongodb-automation/server.pem`                                   | `util.PEMKeyFilePathInContainer`            |
| `args2_6.net.tls.PEMKeyFile`         | `/mongodb-automation/server.pem`                                   | `util.PEMKeyFilePathInContainer`            |
| `args2_6.net.ssl.certificateKeyFile` | `/mongodb-automation/server.pem`                                   | `util.PEMKeyFilePathInContainer`            |
| `args2_6.net.ssl.PEMKeyFile`         | `/mongodb-automation/server.pem`                                   | `util.PEMKeyFilePathInContainer`            |
| `args2_6.net.tls.clusterFile`        | `/mongodb-automation/cluster-auth/<processName>-pem`               | `util.InternalClusterAuthMountPath`         |
| `args2_6.net.ssl.clusterFile`        | `/mongodb-automation/cluster-auth/<processName>-pem`               | `util.InternalClusterAuthMountPath`         |
| `tls.autoPEMKeyFilePath`             | *(must not be set)*                                                | —                                           |
| `tls.CAFilePath`                     | `/mongodb-automation/tls/ca/ca-pem`                                | `util.TLSCaMountPath + "/ca-pem"`           |
| `monitoringAgentConfig.logPath`      | `/var/log/mongodb-mms-automation/monitoring-agent.log`             | `util.PvcMountPathLogs + "/monitoring-agent.log"` |
| `backupAgentConfig.logPath`          | `/var/log/mongodb-mms-automation/backup-agent.log`                 | `util.PvcMountPathLogs + "/backup-agent.log"` |
| `options.downloadBase`               | `/var/lib/mongodb-mms-automation`                                  | `util.PvcMmsMountPath`                      |
| `processes[].processType`            | `mongod`                                                           | —                                           |
| `processes[].authSchemaVersion`      | `5`                                                                | `om.CalculateAuthSchemaVersion()`           |
| `args2_6.sharding.clusterRole`       | *(must not be set)*                                                | —                                           |
| `replicaSets[].protocolVersion`      | `"1"`                                                              | —                                           |
