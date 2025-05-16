[//]: # (These are the legacy release notes for MongoDB Enterprise Kubernetes Operator. The new release notes are now part of the MCK release notes. The MCK release notes are available at RELEASE_NOTES.md)
<!-- Next Release -->

# MongoDB Enterprise Kubernetes Operator 1.33.0

## New Features
* **MongoDBOpsManager**, **AppDB**: Introduced support for OpsManager and Application Database deployments across multiple Kubernetes clusters without requiring a Service Mesh.
    * New property [spec.applicationDatabase.externalAccess](TBD) used for common service configuration or in single cluster deployments
    * Added support for existing, but unused property [spec.applicationDatabase.clusterSpecList.externalAccess](TBD)
    * You can define annotations for external services managed by the operator that contain placeholders which will be automatically replaced to the proper values:
        * AppDB: [spec.applicationDatabase.externalAccess.externalService.annotations](TBD)
        * MongoDBOpsManager: Due to different way of configuring external service placeholders are not yet supported
    * More details can be found in the [public documentation](TBD).

## Bug Fixes
* Fixed a bug where workloads in the `static` container architecture were still downloading binaries. This occurred when the operator was running with the default container architecture set to `non-static`, but the workload was deployed with the `static` architecture using the `mongodb.com/v1.architecture: "static"` annotation.
* **MongoDB**: Operator now correctly applies the external service customization based on `spec.externalAccess` and `spec.mongos.clusterSpecList.externalAccess` configuration. Previously it was ignored, but only for Multi Cluster Sharded Clusters.
* **OpsManager**: Ops Manager resources were not properly cleaned up on deletion. The operator now ensures that all resources are removed when the Ops Manager resource is deleted.
* **AppDB**: Fixed an issue with wrong monitoring hostnames for `Application Database` deployed in multi-cluster mode. Monitoring agents should discover the correct hostnames and send data back to `Ops Manager`. The hostnames used for monitoring AppDB in Multi-Cluster deployments with a service mesh are `{resource_name}-db-{cluster_index}-{pod_index}-svc.{namespace}.svc.{cluster_domain}`. TLS certificate should be defined for these hostnames.
    * **NOTE (Multi-Cluster)** This bug fix will result in the loss of historical monitoring data for multi-cluster AppDB members. If retaining this data is critical, please back it up before upgrading. This only affects monitoring data for multi-cluster AppDB deployments — it does not impact single-cluster AppDBs or any other MongoDB deployments managed by this Ops Manager instance.
        * To export the monitoring data of AppDB members, please refer to the Ops Manager API reference https://www.mongodb.com/docs/ops-manager/current/reference/api/measures/get-host-process-system-measurements/
* **OpsManager**: Fixed a bug where the `spec.clusterSpecList.externalConnectivity` field was not being used by the operator, but documented in Ops Manager API reference https://www.mongodb.com/docs/kubernetes-operator/current/reference/k8s-operator-om-specification/#mongodb-opsmgrkube-opsmgrkube.spec.clusterSpecList.externalConnectivity
* **OpsManager**: Fixed a bug where a custom CA would always be expected when configuring Ops Manager with TLS enabled.

## Breaking Change
* **Images**: Removing all references of the images and repository of `mongodb-enterprise-appdb-database-ubi`, as it has been deprecated since version 1.22.0. This means, we won't rebuild images in that repository anymore nor add RELATED_IMAGES_*.
<!-- Past Releases -->

# MongoDB Enterprise Kubernetes Operator 1.32.0

## New Features
* **General Availability - Multi Cluster Sharded Clusters:** Support configuring highly available MongoDB Sharded Clusters across multiple Kubernetes clusters.
    - `MongoDB` resources of type Sharded Cluster now support both single and multi cluster topologies.
    - The implementation is backwards compatible with single cluster deployments of MongoDB Sharded Clusters, by defaulting `spec.topology` to `SingleCluster`. Existing `MongoDB` resources do not need to be modified to upgrade to this version of the operator.
    - Introduced support for Sharded deployments across multiple Kubernetes clusters without requiring a Service Mesh - the is made possible by enabling all components of such a deployment (including mongos, config servers, and mongod) to be exposed externally to the Kubernetes clusters, which enables routing via external interfaces.
    - More details can be found in the [public documentation](https://www.mongodb.com/docs/kubernetes-operator/current/reference/k8s-operator-specification/#sharded-cluster-settings).
* Adding opt-out anonymized telemetry to the operator. The data does not contain any Personally Identifiable Information
  (PII) or even data that can be tied back to any specific customer or company. More can be read [public documentation](https://www.mongodb.com/docs/kubernetes-operator/current/reference/meko-telemetry), this link further elaborates on the following topics:
    * What data is included in the telemetry
    * How to disable telemetry
    * What RBACs are added and why they are required
* **MongoDB**: To ensure the correctness of scaling operations, a new validation has been added to Sharded Cluster deployments. This validation restricts scaling different components in two directions simultaneously within a single change to the YAML file. For example, it is not allowed to add more nodes (scaling up) to shards while simultaneously removing (scaling down) config servers or mongos. This restriction also applies to multi-cluster deployments. A simple change that involves "moving" one node from one cluster to another—without altering the total number of members—will now be blocked. It is necessary to perform a scale-up operation first and then execute a separate change for scaling down.

## Bug Fixes
* Fixes the bug when status of `MongoDBUser` was being set to `Updated` prematurely. For example, new users were not immediately usable following `MongoDBUser` creation despite the operator reporting `Updated` state.
* Fixed a bug causing cluster health check issues when ordering of users and tokens differed in Kubeconfig.
* Fixed a bug when deploying a Multi-Cluster sharded resource with an external access configuration could result in pods not being able to reach each others.
* Fixed a bug when setting `spec.fcv = AlwaysMatchVersion` and `agentAuth` to be `SCRAM` causes the operator to set the auth value to be `SCRAM-SHA-1` instead of `SCRAM-SHA-256`.

# MongoDB Enterprise Kubernetes Operator 1.31.0

## Kubernetes versions
* The minimum supported Kubernetes version for this operator is 1.30 and OpenShift 4.17.

## Bug Fixes
* Fixed handling proxy environment variables in the operator pod. The environment variables [`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`] when set on the operator pod, can now be propagated to the MongoDB agents by also setting the environment variable `MDB_PROPAGATE_PROXY_ENV=true`.


# MongoDB Enterprise Kubernetes Operator 1.30.0

## New Features

* **MongoDB**: fixes and improvements to Multi-Cluster Sharded Cluster deployments (Public Preview)
* **MongoDB**: `spec.shardOverrides` field, which was added in 1.28.0 as part of Multi-Cluster Sharded Cluster Public Preview is now fully supported for single-cluster topologies and is the recommended way of customizing settings for specific shards.
* **MongoDB**: `spec.shardSpecificPodSpec` was deprecated. The recommended way of customizing specific shard settings is to use `spec.shardOverrides` for both Single and Multi Cluster topology. An example of how to migrate the settings to spec.shardOverrides is available [here](https://github.com/mongodb/mongodb-enterprise-kubernetes/blob/master/samples/sharded_multicluster/shardSpecificPodSpec_migration.yaml).

## Bug Fixes
* **MongoDB**: Fixed placeholder name for `mongos` in Single Cluster Sharded with External Domain set. Previously it was called `mongodProcessDomain` and `mongodProcessFQDN` now they're called `mongosProcessDomain` and `mongosProcessFQDN`.
* **MongoDB**, **MongoDBMultiCluster**, **MongoDBOpsManager**: In case of losing one of the member clusters we no longer emit validation errors if the failed cluster still exists in the `clusterSpecList`. This allows easier reconfiguration of the deployments as part of disaster recovery procedure.

## Kubernetes versions
* The minimum supported Kubernetes version for this operator is 1.29 and OpenShift 4.17.

# MongoDB Enterprise Kubernetes Operator 1.29.0

## New Features
* **AppDB**: Added support for easy resize. More can be read in changelog 1.28.0 - "automated expansion of the pvc"

## Bug Fixes

* **MongoDB**, **AppDB**, **MongoDBMultiCluster**: Fixed a bug where specifying a fractional number for a storage volume's size such as `1.7Gi` can break the reconciliation loop for that resource with an error like `Can't execute update on forbidden fields` even if the underlying Persistence Volume Claim is deployed successfully.
* **MongoDB**, **MongoDBMultiCluster**, **OpsManager**, **AppDB**: Increased stability of deployments during TLS rotations. In scenarios where the StatefulSet of the deployment was reconciling and a TLS rotation happened, the deployment would reach a broken state. Deployments will now store the previous TLS certificate alongside the new one.

# MongoDB Enterprise Kubernetes Operator 1.28.0

## New Features

* **MongoDB**: public preview release of multi kubernetes cluster support for sharded clusters. This can be enabled by setting `spec.topology=MultiCluster` when creating `MongoDB` resource of `spec.type=ShardedCluster`. More details can be found [here](https://www.mongodb.com/docs/kubernetes-operator/master/multi-cluster-sharded-cluster/).
* **MongoDB**, **MongoDBMultiCluster**: support for automated expansion of the PVC.
  More details can be found [here](https://www.mongodb.com/docs/kubernetes-operator/upcoming/tutorial/resize-pv-storage/).
  **Note**: Expansion of the pvc is only supported if the storageClass supports expansion.
  Please ensure that the storageClass supports in-place expansion without data-loss.
    * **MongoDB** This can be done by increasing the size of the PVC in the CRD setting:
        * one PVC - increase: `spec.persistence.single.storage`
        * multiple PVCs - increase: `spec.persistence.multiple.(data/journal/logs).storage`
    * **MongoDBMulti** This can be done by increasing the storage via the statefulset override:
```yaml
  statefulSet:
    spec:
      volumeClaimTemplates:
        - metadata:
            name: data
          spec:
            resources:
              requests:
                storage: 2Gi # this is my increased storage
                storageClass: <my-class-that-supports-expansion>
```
* **MongoDB**, **MongoDBMultiCluster** **AppDB**: change default behaviour of setting featurecompatibilityversion (fcv) for the database.
    * When upgrading mongoDB version the operator sets the FCV to the prior version we are upgrading from. This allows to
      have sanity checks before setting the fcv to the upgraded version. More information can be found [here](https://www.mongodb.com/docs/kubernetes-operator/current/reference/k8s-operator-specification/#mongodb-setting-spec.featureCompatibilityVersion).
    * To keep the prior behaviour to always use the mongoDB version as FCV; set `spec.featureCompatibilityVersion: "AlwaysMatchVersion"`
* Docker images are now built with `ubi9` as the base image with the exception of [mongodb-enterprise-database-ubi](quay.io/mongodb/mongodb-enterprise-database-ubi) which is still based on `ubi8` to support `MongoDB` workloads < 6.0.4. The `ubi8` image is only in use for the default non-static architecture.
  For a full `ubi9` setup, the [Static Containers](https://www.mongodb.com/docs/kubernetes-operator/upcoming/tutorial/plan-k8s-op-container-images/#static-containers--public-preview-) architecture should be used instead.
* **OpsManager**: Introduced support for Ops Manager 8.0.0
* **MongoDB**, **MongoDBMulti**: support for MongoDB 8.0.0
## Bug Fixes

* **MongoDB**, **AppDB**, **MongoDBMultiCluster**: Fixed a bug where the init container was not getting the default security context, which was flagged by security policies.
* **MongoDBMultiCluster**: Fixed a bug where resource validations were not performed as part of the reconcile loop.

# MongoDB Enterprise Kubernetes Operator 1.27.0

## New Features

* **MongoDB** Added Support for enabling LogRotation for MongoDB processes, MonitoringAgent and BackupAgent. More can be found in the following [documentation](LINK TO DOCS).
    * `spec.agent.mongod.logRotation` to configure the mongoDB processes
    * `spec.agent.mongod.auditLogRotation` to configure the mongoDB processes audit logs
    * `spec.agent.backupAgent.logRotation` to configure the backup agent
    * `spec.agent.monitoringAgent.logRotation` to configure the backup agent
    * `spec.agent.readinessProbe.environmentVariables` to configure the environment variables the readinessProbe runs with.
      That also applies to settings related to the logRotation,
      the supported environment settings can be found [here](https://github.com/mongodb/mongodb-kubernetes-operator/blob/master/docs/logging.md#readinessprobe).
    * the same applies for AppDB:
        * you can configure AppDB via `spec.applicationDatabase.agent.mongod.logRotation`
    * Please Note: For shardedCluster we only support configuring logRotation under `spec.Agent`
      and not per process type (mongos, configsrv etc.)

* **Opsmanager** Added support for replacing the logback.xml which configures general logging settings like logRotation
    * `spec.logging.LogBackAccessRef` points at a ConfigMap/key with the logback access configuration file to mount on the Pod
        * the key of the configmap has to be `logback-access.xml`
    * `spec.logging.LogBackRef` points at a ConfigMap/key with the logback access configuration file to mount on the Pod
        * the key of the configmap has to be `logback.xml`

## Deprecations

* **AppDB** logRotate for appdb has been deprecated in favor for the new field
    * this `spec.applicationDatabase.agent.logRotation` has been deprecated for `spec.applicationDatabase.agent.mongod.logRotation`

## Bug Fixes

* **Agent** launcher: under some resync scenarios we can have corrupted journal data in `/journal`.
  The agent now makes sure that there are not conflicting journal data and prioritizes the data from `/data/journal`.
    * To deactivate this behaviour set the environment variable in the operator `MDB_CLEAN_JOURNAL`
      to any other value than 1.
* **MongoDB**, **AppDB**, **MongoDBMulti**: make sure to use external domains in the connectionString created if configured.

* **MongoDB**: Removed panic response when configuring shorter horizon config compared to number of members. The operator now signals a
  descriptive error in the status of the **MongoDB** resource.

* **MongoDB**: Fixed a bug where creating a resource in a new project named as a prefix of another project would fail, preventing the `MongoDB` resource to be created.

# MongoDB Enterprise Kubernetes Operator 1.26.0

## New Features

* Added the ability to control how many reconciles can be performed in parallel by the operator.
  This enables strongly improved cpu utilization and vertical scaling of the operator and will lead to quicker reconcile of all managed resources.
    * It might lead to increased load on the Ops Manager and K8s API server in the same time window.
      by setting `MDB_MAX_CONCURRENT_RECONCILES` for the operator deployment or `operator.maxConcurrentReconciles` in the operator's Helm chart.
      If not provided, the default value is 1.
        * Observe the operator's resource usage and adjust (`operator.resources.requests` and `operator.resources.limits`) if needed.

## Helm Chart

* New `operator.maxConcurrentReconciles` parameter. It controls how many reconciles can be performed in parallel by the operator. The default value is 1.
* New `operator.webhook.installClusterRole` parameter. It controls whether to install the cluster role allowing the operator to configure admission webhooks. It should be disabled when cluster roles are not allowed. Default value is true.

## Bug Fixes

* **MongoDB**: Fixed a bug where configuring a **MongoDB** with multiple entries in `spec.agent.startupOptions` would cause additional unnecessary reconciliation of the underlying `StatefulSet`.
* **MongoDB, MongoDBMultiCluster**: Fixed a bug where the operator wouldn't watch for changes in the X509 certificates configured for agent authentication.
* **MongoDB**: Fixed a bug where boolean flags passed to the agent cannot be set to `false` if their default value is `true`.

# MongoDB Enterprise Kubernetes Operator 1.25.0

## New Features

* **MongoDBOpsManager**: Added support for deploying Ops Manager Application on multiple Kubernetes clusters. See [documentation](LINK TO DOCS) for more information.
* (Public Preview) **MongoDB, OpsManager**: Introduced opt-in Static Architecture (for all types of deployments) that avoids pulling any binaries at runtime.
  * This feature is recommended only for testing purposes, but will become the default in a later release.
  * You can activate this mode by setting the `MDB_DEFAULT_ARCHITECTURE` environment variable at the Operator level to `static`. Alternatively, you can annotate a specific `MongoDB` or `OpsManager` Custom Resource with `mongodb.com/v1.architecture: "static"`.
    * The Operator supports seamless migration between the Static and non-Static architectures.
    * To learn more please see the relevant documentation:
        * [Use Static Containers](https://www.mongodb.com/docs/kubernetes-operator/stable/tutorial/plan-k8s-op-considerations/#use-static-containers--beta-)
        * [Migrate to Static Containers](https://www.mongodb.com/docs/kubernetes-operator/stable/tutorial/plan-k8s-op-container-images/#migrate-to-static-containers)
* **MongoDB**: Recover Resource Due to Broken Automation Configuration has been extended to all types of MongoDB resources, now including Sharded Clusters. For more information see https://www.mongodb.com/docs/kubernetes-operator/master/reference/troubleshooting/#recover-resource-due-to-broken-automation-configuration
* **MongoDB, MongoDBMultiCluster**: Placeholders in external services.
    * You can now define annotations for external services managed by the operator that contain placeholders which will be automatically replaced to the proper values.
    * Previously, the operator was configuring the same annotations for all external services created for each pod. Now, with placeholders the operator is able to customize
      annotations in each service with values that are relevant and different for the particular pod.
    * To learn more please see the relevant documentation:
        * MongoDB: [spec.externalAccess.externalService.annotations](https://www.mongodb.com/docs/kubernetes-operator/stable/reference/k8s-operator-specification/#mongodb-setting-spec.externalAccess.externalService.annotations)
        * MongoDBMultiCluster: [spec.externalAccess.externalService.annotations](https://www.mongodb.com/docs/kubernetes-operator/stable/reference/k8s-operator-multi-cluster-specification/#mongodb-setting-spec.externalAccess.externalService.annotations)
*  `kubectl mongodb`:
* Added printing build info when using the plugin.
* `setup` command:
    * Added `--image-pull-secrets` parameter. If specified, created service accounts will reference the specified secret on `ImagePullSecrets` field.
    * Improved handling of configurations when the operator is installed in a separate namespace than the resources it's watching and when the operator is watching more than one namespace.
    * Optimized roles and permissions setup in member clusters, using a single service account per cluster with correctly configured Role and RoleBinding (no ClusterRoles necessary) for each watched namespace.
* **OpsManager**: Added the `spec.internalConnectivity` field to allow overrides for the service used by the operator to ensure internal connectivity to the `OpsManager` pods.
* Extended the existing event based reconciliation by a time-based one, that is triggered every 24 hours. This ensures all Agents are always upgraded on timely manner.
* OpenShift / OLM Operator: Removed the requirement for cluster-wide permissions. Previously, the operator needed these permissions to configure admission webhooks. Now, webhooks are automatically configured by [OLM](https://olm.operatorframework.io/docs/advanced-tasks/adding-admission-and-conversion-webhooks/).
* Added optional `MDB_WEBHOOK_REGISTER_CONFIGURATION` environment variable for the operator. It controls whether the operator should perform automatic admission webhook configuration. Default: true. It's set to false for OLM and OpenShift deployments.

## Breaking Change

* **MongoDBOpsManager** Stopped testing against Ops Manager 5.0. While it may continue to work, we no longer officially support Ops Manager 5 and customers should move to a later version.

## Helm Chart

* New `operator.webhook.registerConfiguration` parameter. It controls whether the operator should perform automatic admission webhook configuration (by setting `MDB_WEBHOOK_REGISTER_CONFIGURATION` environment variable for the operator). Default: true. It's set to false for OLM and OpenShift deployments.
* Changing the default `agent.version` to `107.0.0.8502-1`, that will change the default agent used in helm deployments.
* Added `operator.additionalArguments` (default: []) allowing to pass additional arguments for the operator binary.
* Added `operator.createResourcesServiceAccountsAndRoles` (default: true) to control whether to install roles and service accounts for MongoDB and Ops Manager resources. When `mongodb kubectl` plugin is used to configure the operator for multi-cluster deployment, it installs all necessary roles and service accounts. Therefore, in some cases it is required to not install those roles using the operator's helm chart to avoid clashes.

## Bug Fixes

* **MongoDBMultiCluster**: Fields `spec.externalAccess.externalDomain` and `spec.clusterSpecList[*].externalAccess.externalDomains` were reported as required even though they weren't
  used. Validation was triggered prematurely when structure `spec.externalAccess` was defined. Now, uniqueness of external domains will only be checked when the external domains are
  actually defined in `spec.externalAccess.externalDomain` or `spec.clusterSpecList[*].externalAccess.externalDomains`.
* **MongoDB**: Fixed a bug where upon deleting a **MongoDB** resource the `controlledFeature` policies are not unset on the related OpsManager/CloudManager instance, making cleanup in the UI impossible in the case of losing the kubernetes operator.
* **OpsManager**: The `admin-key` Secret is no longer deleted when removing the OpsManager Custom Resource. This enables easier Ops Manager re-installation.
* **MongoDB ReadinessProbe** Fixed the misleading error message of the readinessProbe: `"... kubelet  Readiness probe failed:..."`. This affects all mongodb deployments.
* **Operator**: Fixed cases where sometimes while communicating with Opsmanager the operator skipped TLS verification, even if it was activated.

## Improvements

**Kubectl plugin**: The released plugin binaries are now signed, the signatures are published with the [release assets](https://github.com/mongodb/mongodb-enterprise-kubernetes/releases). Our public key is available at [this address](https://cosign.mongodb.com/mongodb-enterprise-kubernetes-operator.pem). They are also notarized for MacOS.
**Released Images signed**: All container images published for the enterprise operator are cryptographically signed. This is visible on our Quay registry, and can be verified using our public key. It is available at [this address](https://cosign.mongodb.com/mongodb-enterprise-kubernetes-operator.pem).


# MongoDB Enterprise Kubernetes Operator 1.24.0

## New Features
* **MongoDBOpsManager**: Added support for the upcoming 7.0.x series of Ops Manager Server.

## Bug Fixes
* Fix a bug that prevented terminating backup correctly.

# MongoDB Enterprise Kubernetes Operator 1.23.0
## Warnings and Breaking Changes

* Starting from 1.23 component image version numbers will be aligned to the MongoDB Enterprise Operator release tag. This allows clear identification of all images related to a specific version of the Operator. This affects the following images:
    * `quay.io/mongodb/mongodb-enterprise-database-ubi`
    * `quay.io/mongodb/mongodb-enterprise-init-database-ubi`
    * `quay.io/mongodb/mongodb-enterprise-init-appdb-ubi`
    * `quay.io/mongodb/mongodb-enterprise-init-ops-manager-ubi`
* Removed `spec.exposedExternally` in favor of `spec.externalAccess` from the MongoDB Customer Resource. `spec.exposedExternally` was deprecated in operator version 1.19.

## Bug Fixes
* Fix a bug with scaling a multi-cluster replica-set in the case of losing connectivity to a member cluster. The fix addresses both the manual and automated recovery procedures.
* Fix of a bug where changing the names of the automation agent and MongoDB audit logs prevented them from being sent to Kubernetes pod logs. There are no longer restrictions on MongoDB audit log file names (mentioned in the previous release).
* New log types from the `mongodb-enterprise-database` container are now streamed to Kubernetes logs.
    * New log types:
        * agent-launcher-script
        * monitoring-agent
        * backup-agent
    * The rest of available log types:
        * automation-agent-verbose
        * automation-agent-stderr
        * automation-agent
        * mongodb
        * mongodb-audit
* **MongoDBUser** Fix a bug ignoring the `Spec.MongoDBResourceRef.Namespace`. This prevented storing the user resources in another namespace than the MongoDB resource.

# MongoDB Enterprise Kubernetes Operator 1.22.0
## Breaking Changes
* **All Resources**: The Operator no longer uses the "Reconciling" state. In most of the cases it has been replaced with "Pending" and a proper message

## Deprecations
None

## Bug Fixes
* **MongoDB**: Fix support for setting `autoTerminateOnDeletion=true` for sharded clusters. This setting makes sure that the operator stops and terminates the backup before the cleanup.

## New Features
* **MongoDB**: An Automatic Recovery mechanism has been introduced for `MongoDB` resources and is turned on by default. If a Custom Resource remains in `Pending` or `Failed` state for a longer period of time (controlled by `MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S` environment variable at the Operator Pod spec level, the default is 20 minutes)
  the Automation Config is pushed to the Ops Manager. This helps to prevent a deadlock when an Automation Config can not be pushed because of the StatefulSet not being ready and the StatefulSet being not ready because of a broken Automation Config.
  The behavior can be turned off by setting `MDB_AUTOMATIC_RECOVERY_ENABLE` environment variable to `false`.
* **MongoDB**: MongoDB audit logs can now be routed to Kubernetes pod logs.
    * Ensure MongoDB audit logs are written to `/var/log/mongodb-mms-automation/mongodb-audit.log` file. Pod monitors this file and tails its content to k8s logs.
    * Use the following example configuration in MongoDB resource to send audit logs to k8s logs:
  ```
  spec:
    additionalMongodConfig:
      auditLog:
        destination: file
        format: JSON
        path: /var/log/mongodb-mms-automation/mongodb-audit.log
  ```
    * Audit log entries are tagged with the "mongodb-audit" key in pod logs. Extract audit log entries with the following example command:
  ```
  kubectl logs -c mongodb-enterprise-database replica-set-0 | jq -r 'select(.logType == "mongodb-audit") | .contents'
  ```
* **MongoDBOpsManager**: Improved handling of unreachable clusters in AppDB Multi-Cluster resources
    * In the last release, the operator required a healthy connection to the cluster to scale down processes, which could block the reconcile process if there was a full-cluster outage.
    * Now, the operator will still successfully manage the remaining healthy clusters, as long as they have a majority of votes to elect a primary.
    * The associated processes of an unreachable cluster are not automatically removed from the automation config and replica set configuration. These processes will only be removed under the following conditions:
        * The corresponding cluster is deleted from `spec.applicationDatabase.clusterSpecList` or has zero members specified.
        * When deleted, the operator scales down the replica set by removing processes tied to that cluster one at a time.
* **MongoDBOpsManager**: Add support for configuring [logRotate](https://www.mongodb.com/docs/ops-manager/current/reference/cluster-configuration/#mongodb-instances) on the automation-agent for appdb.
* **MongoDBOpsManager**: [systemLog](https://www.mongodb.com/docs/manual/reference/configuration-options/#systemlog-options) can now be configured to differ from the otherwise default of `/var/log/mongodb-mms-automation`.

# MongoDB Enterprise Kubernetes Operator 1.21.0
## Breaking changes
* The environment variable to track the operator namespace has been renamed from [CURRENT_NAMESPACE](https://github.com/mongodb/mongodb-enterprise-kubernetes/blob/master/mongodb-enterprise.yaml#L244) to `NAMESPACE`. If you set this variable manually via YAML files, you should update this environment variable name while upgrading the operator deployment.

## Bug fixes
* Fixes a bug where passing the labels via statefulset override mechanism would not lead to an override on the actual statefulset.

## New Feature
* Support for Label and Annotations Wrapper for the following CRDs: mongodb, mongodbmulti and opsmanager
    * Additionally, to the `specWrapper` for `statefulsets` we now support overriding `metadata.Labels` and `metadata.Annotations` via the `MetadataWrapper`.

# MongoDBOpsManager Resource

## New Features
* Support configuring `OpsManager` with a highly available `applicationDatabase` across multiple Kubernetes clusters by introducing the following fields:
    - `om.spec.applicationDatabase.topology` which can be one of `MultiCluster` and `SingleCluster`.
    - `om.spec.applicationDatabase.clusterSpecList` for configuring the list of Kubernetes clusters which will have For extended considerations for the multi-cluster AppDB configuration, check [the official guide](https://www.mongodb.com/docs/kubernetes-operator/stable/tutorial/plan-om-resource.html#using-onprem-with-multi-kubernetes-cluster-deployments) and the `OpsManager` [resource specification](https://www.mongodb.com/docs/kubernetes-operator/stable/reference/k8s-operator-om-specification/#k8s-om-specification).
      The implementation is backwards compatible with single cluster deployments of AppDB, by defaulting `om.spec.applicationDatabase.topology` to `SingleCluster`. Existing `OpsManager` resources do not need to be modified to upgrade to this version of the operator.
* Support for providing a list of custom certificates for S3 based backups via secret references `spec.backup.[]s3Stores.customCertificateSecretRefs` and `spec.backup.[]s3OpLogStores.customCertificateSecretRefs`
    * The list consists of single certificate strings, each references a secret containing a certificate authority.
    * We do not support adding multiple certificates in a chain. In that case, only the first certificate in the chain is imported.
    * Note:
        * If providing a list of `customCertificateSecretRefs`, then those certificates will be used instead of the default certificates setup in the JVM Trust Store (in Ops Manager or Cloud Manager).
        * If none are provided, the default JVM Truststore certificates will be used instead.

## Breaking changes
* The `appdb-ca` is no longer automatically added to the JVM Trust Store (in Ops Manager or Cloud Manager). Since a bug introduced in version `1.17.0`, automatically adding these certificates to the JVM Trust Store has no longer worked.
    * This will only impact you if:
        * You are using the same custom certificate for both appdb-ca and for your S3 compatible backup store
        * AND: You are using an operator prior to `1.17.0` (where automated inclusion in the JVM Trust Store worked) OR had a workaround (such as mounting your own trust store to OM)
    * If you do need to use the same custom certificate for both appdb-ca and for your S3 compatible backup store then you now need to utilise `spec.backup.[]s3Config.customCertificateSecretRefs` (introduced in this release and covered below in the release notes) to specify the certificate authority for use for backups.
    * The `appdb-ca` is the certificate authority saved in the configmap specified under `om.spec.applicationDatabase.security.tls.ca`.

## Bug fixes
* Allowed setting an arbitrary port number in `spec.externalConnectivity.port` when `LoadBalancer` service type is used for exposing Ops Manager instance externally.
* The operator is now able to import the `appdb-ca` which consists of a bundle of certificate authorities into the ops-manager JVM trust store. Previously, the keystore had 2 problems:
    * It was immutable.
    * Only the first certificate authority out of the bundle was imported into the trust store.
    * Both could lead to certificates being rejected by Ops Manager during requests to it.

## Deprecation
* The setting `spec.backup.[]s3Stores.customCertificate` and `spec.backup.[]s3OpLogStores.customCertificate` are being deprecated in favor of `spec.backup.[]s3OpLogStores.[]customCertificateSecretRefs` and `spec.backup.[]s3Stores.[]customCertificateSecretRefs`
    * Previously, when enabling `customCertificate`, the operator would use the `appdb-ca` as the custom certificate. Currently, this should be explicitly set via `customCertificateSecretRefs`.

## New Features
* Support for providing a list of custom certificates for S3 based backups via secret references `spec.backup.[]s3Stores.customCertificateSecretRefs` and `spec.backup.[]s3OpLogStores.customCertificateSecretRefs`
    * The list consists of single certificate strings, each references a secret containing a certificate authority.
    * We do not support adding multiple certificates in a chain. In that case, only the first certificate in the chain is imported.
    * Note:
        * If providing a list of `customCertificateSecretRefs`, then those certificates will be used instead of the default certificates setup in the JVM Trust Store (in Ops Manager or Cloud Manager).
        * If none are provided, the default JVM Truststore certificates will be used instead.

# MongoDB Enterprise Kubernetes Operator 1.20.1

This release fixes an issue that prevented upgrading the Kubernetes Operator to 1.20.0 in OpenShift. Upgrade to this release instead.

## Helm Chart
Fixes a bug where the operator container image was referencing to the deprecated ubuntu image. This has been patched to refer to the `ubi` based images.

# MongoDB Enterprise Kubernetes Operator 1.20.0

# MongoDBOpsManager Resource
* Added support for votes, priority and tags by introducing the `spec.applicationDatabase.memberConfig.votes`, `spec.applicationDatabase.memberConfig.priority`
  and `spec.applicationDatabase.memberConfig.tags` field.
* Introduced automatic change of the AppDB's image version suffix `-ent` to `-ubi8`.
    * This enables migration of AppDB images from the legacy repository (`quay.io/mongodb/mongodb-enterprise-appdb-database-ubi`) to the new official one (`quay.io/mongodb/mongodb-enterprise-server`) without changing the version in MongoDBOpsManager's `applicationDatabase.version` field.
    * The change will result a rolling update of AppDB replica set pods to the new, official images (referenced in Helm Chart in `values.mongodb.name` field), which are functionally equivalent to the previous ones (the same MongoDB version).
    * Suffix change occurs under specific circumstances:
        * Helm setting for appdb image: `mongodb.name` will now default to `mongodb-enterprise-server`.
        * The operator will automatically replace the suffix for image repositories
          that end with `mongodb-enterprise-server`.
          Operator will replace the suffix `-ent` with the value set in the environment variable
          `MDB_IMAGE_TYPE`, which defaults to `-ubi8`.
          For instance, the operator will migrate:
            * `quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent` to `quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubi8`.
            * `MDB_IMAGE_TYPE=ubuntu2024 quay.io/mongodb/mongodb-enterprise-server:4.2.11-ent` to `quay.io/mongodb/mongodb-enterprise-server:4.2.11-ubuntu2024`.
            * The operator will do the automatic migration of suffixes only for images
              that reference the name `mongodb-enterprise-server`.
              It won't perform migration for any other image name, e.g.:
                * `mongodb-enterprise-appdb-database-ubi:4.0.0-ent` will not be altered
            * To stop the automatic suffix migration behavior,
              set the following environment variable to true: `MDB_APPDB_ASSUME_OLD_FORMAT=true`
              or alternatively in the following helm chart setting: `mongodb.appdbAssumeOldFormat=true`
* Added support for defining bare versions in `spec.applicationDatabase.version`. Previously, it was required to specify AppDB's version with `-ent` suffix. Currently, it is possible to specify a bare version, e.g. `6.0.5` and the operator will convert it to `6.0.5-${MDB_IMAGE_TYPE}`. The default for environment variable `MDB_IMAGE_TYPE` is `-ubi8`.

## Bug fixes
* Fixed MongoDBMultiCluster not watching Ops Manager's connection configmap and secret.
* Fixed support for rotating the clusterfile secret, which is used for internal x509 authentication in MongoDB and MongoDBMultiCluster resources.

## Helm Chart
* All images reference ubi variants by default (added suffix -ubi)
    * quay.io/mongodb/mongodb-enterprise-database-ubi
    * quay.io/mongodb/mongodb-enterprise-init-database-ubi
    * quay.io/mongodb/mongodb-enterprise-ops-manager-ubi
    * quay.io/mongodb/mongodb-enterprise-init-ops-manager-ubi
    * quay.io/mongodb/mongodb-enterprise-init-appdb-ubi
    * quay.io/mongodb/mongodb-agent-ubi
    * quay.io/mongodb/mongodb-enterprise-appdb-database-ubi
* Changed default AppDB repository to official MongoDB Enterprise repository in `values.mongodb.name` field: quay.io/mongodb/mongodb-enterprise-server.
* Introduced `values.mongodb.imageType` variable to specify a default image type suffix added to AppDB's version used by MongoDBOpsManager resource.

## Breaking changes
* Removal of `appdb.connectionSpec.Project` since it has been deprecated for over 2 years.

# MongoDB Enterprise Kubernetes Operator 1.19.0

## MongoDB Resource
* Added support for setting replica set member votes by introducing the `spec.memberOptions.[*].votes` field.
* Added support for setting replica set member priority by introducing the `spec.memberOptions.[*].priority` field.
* Added support for setting replica set member tags by introducing the `spec.memberOptions.[*].tags` field.

## MongoDBMulti Resource
* Added support for setting replica set member votes by introducing the `spec.clusterSpecList.[*].memberOptions.[*].votes` field.
* Added support for setting replica set member priority by introducing the `spec.clusterSpecList.[*].memberOptions.[*].priority` field.
* Added support for setting replica set member tags by introducing the `spec.clusterSpecList.[*].memberOptions.[*].tags` field.

## Improvements

* New guidance for multi-Kubernetes-cluster deployments without a Service Mesh. It covers use of a Load Balancer Service
  to expose ReplicaSet members on an externally reachable domain (`spec.externalAccess.externalDomain`).
  This leverages setting the `process.hostname` field in the Automation Config.
  [This tutorial](ttps://www.mongodb.com/docs/kubernetes-operator/v1.19/tutorial/proper_link) provides full guidance.
*  `spec.security.authentication.ldap.transportSecurity`: "none" is now a valid configuration to use no transportSecurity.
* Allows you to configure `podSpec` per shard in a MongoDB Sharded cluster by specifying an array of `podSpecs` under `spec.shardSpecificPodSpec` for each shard.

## Deprecations

* Making the field orgId in the project configmap a requirement. **Note**: If explicitly an empty `orgId = ""` has been chosen then OM will try to create an ORG with the project name.
* Ubuntu-based images were deprecated in favor of UBI-based images in operator version 1.17.0. In the 1.19.0 release we are removing the support for Ubuntu-based images. The ubuntu based images won't be rebuilt daily with updates. Please upgrade to the UBI-based images by following these instructions: https://www.mongodb.com/docs/kubernetes-operator/master/tutorial/migrate-k8s-images/#migrate-k8s-images
* The `spec.exposedExternally` option becomes deprecated in favor of `spec.externalAccess`. The deprecated option will be removed in MongoDB Enterprise Operator 1.22.0.

## Bug fixes
* Fixed handling of `WATCH_NAMESPACE='*'` environment variable for multi-cluster deployments with cluster-wide operator. In some specific circumstances, API clients for member clusters were configured incorrectly resulting in deployment errors.
    * Example error in this case:
        * `The secret object 'mdb-multi-rs-cert' does not contain all the valid certificates needed: secrets "mdb-multi-rs-cert-pem" already exists`
    * These specific circumstances were:
        * `WATCH_NAMESPACE='*'` environment variable passed to the operator deployment
        * specific namespace set in kubeconfig for member clusters
        * not using multi-cluster cli tool for configuring
    * Possible workarounds:
        * set WATCH_NAMESPACE environment variable to specific namespaces instead of '*'
        * make sure that kubeconfigs for member clusters doesn't specify a namespace

## Breaking changes
* Renaming of the multicluster CRD `MongoDBMulti` to `MongoDBMultiCluster`

* The `spec.members` field is required to be set in case of MongoDB deployment of type `ReplicaSet`.
## Bug fixes
* Fixed a panic when `CertificatesSecretsPrefix` was set but no further `spec.security.tls` setting was set i.e. `tls.additionalCertificateDomains` or `tls.ca`.

# MongoDB Enterprise Kubernetes Operator 1.18.0

## Improvements

* Added support for the missing features for Ops Manager Backup configuration page. This includes:
    * KMIP Backup Configuration support by introducing `spec.backup.encryption.kmip` in both OpsManager and MongoDB resources.
    * Backup Assignment Labels settings in `spec.backup.[*].assignmentLabels` elements of the OpsManager resource.
    * Backup Snapshot Schedule configuration via `spec.backup.snapshotSchedule` in the OpsManager resource.
* Added `SCRAM-SHA-1` support for both user and Agent authentication. Before enabling this capability, make sure you use both `MONGODB-CR` and `SCRAM-SHA-1` in the authentication modes.

## Bug fixes
* Fixed liveness probe reporting positive result when the agent process was killed. This could cause database pods to run without automation agent.
* Fixed startup script in database pod, that could in some situations report errors on pod's restart.

## Breaking changes and deprecations

* The field `spec.security.tls.secretRef.prefix` has been removed from MongoDB and OpsManager resources. It was deprecated in the [MongoDB Enterprise
  1.15.0](https://www.mongodb.com/docs/kubernetes-operator/master/release-notes/#k8s-op-full-1-15-0) and removed from the Operator runtime in
  [1.17.0](https://www.mongodb.com/docs/kubernetes-operator/master/release-notes/#k8s-op-full-1-17-0). Before upgrading to this version, make
  sure you migrated to the new TLS format using the following [Migration Guide](https://www.mongodb.com/docs/kubernetes-operator/v1.16/tutorial/migrate-to-new-tls-format/) before upgrading the Operator.

# MongoDB Enterprise Kubernetes Operator 1.17.2

* Fixed the OpenShift installation problem mentioned in the Enterprise Operator 1.7.1 release notes. The OLM (Operator Lifecycle Manager)
  upgrade graph will automatically skip the 1.7.1 release and perform an upgrade from 1.7.0 directly to this release.
* Adds startup probes for database and OpsManager resources with some defaults. This improves the reliability of upgrades by ensuring things occur in the correct order. Customers can also override probe configurations with `podTemplateSpec`.
# MongoDB Enterprise Kubernetes Operator 1.17.1

## Important OpenShift Warning
For OpenShift customers, we recommend that you do NOT upgrade to this release (version 1.17.1), and instead upgrade to version 1.17.2, which is due the week commencing 17th October 2022, or upgrade to later versions.

This release has invalid `quay.io/mongodb/mongodb-agent-ubi` digests referenced in certified bundle's CSV. Installing it could result in ImagePullBackOff errors in AppDB pods (OpsManager's database). Errors will look similar to:
```
  Failed to pull image "quay.io/mongodb/mongodb-agent-ubi@sha256:a4cadf209ab87eb7d121ccd8b1503fa5d88be8866b5c3cb7897d14c36869abf6": rpc error: code = Unknown desc = reading manifest sha256:a4cadf209ab87eb7d121ccd8b1503fa5d88be8866b5c3cb7897d14c36869abf6 in quay.io/mongodb/mongodb-agent-ubi: manifest unknown: manifest unknown
```
This affects only OpenShift deployments when installing/upgrading the Operator version 1.17.1 from the certified bundle (OperatorHub).

The following workaround fixes the issue by replacing the invalid sha256 digests.

### Workaround

If you proceed to use the Operator version 1.17.1 in OpenShift, you must make the following changes. Update the Operator's Subscription with the following `spec.config.env`:
```yaml
spec:
  config:
    env:
      - name: AGENT_IMAGE
        value: >-
          quay.io/mongodb/mongodb-agent-ubi@sha256:ffa842168cc0865bba022b414d49e66ae314bf2fd87288814903d5a430162620
      - name: RELATED_IMAGE_AGENT_IMAGE_11_0_5_6963_1
        value: >-
          quay.io/mongodb/mongodb-agent-ubi@sha256:e7176c627ef5669be56e007a57a81ef5673e9161033a6966c6e13022d241ec9e
      - name: RELATED_IMAGE_AGENT_IMAGE_11_12_0_7388_1
        value: >-
          quay.io/mongodb/mongodb-agent-ubi@sha256:ffa842168cc0865bba022b414d49e66ae314bf2fd87288814903d5a430162620
      - name: RELATED_IMAGE_AGENT_IMAGE_12_0_4_7554_1
        value: >-
          quay.io/mongodb/mongodb-agent-ubi@sha256:3e07e8164421a6736b86619d9d72f721d4212acb5f178ec20ffec045a7a8f855
```

**This workaround should be removed as soon as the new Operator version (>=1.17.2) is installed.**

## MongoDB Operator

## Improvements

* The Red Hat certified operator now uses Quay as an image registry. New images will be automatically pulled upon the operator upgrade and no user action is required as a result of this change.

## Breaking changes

* Removed `operator.deployment_name` from the Helm chart. Parameter was used in an incorrect way and only for customising the name of the operator container. The name of the container is now set to `operator.name`. This is a breaking change only if `operator.deployment_name` was set to a different value than `operator.name` and if there is external tooling relying on this. Otherwise this change will be unnoticeable.

# MongoDB Enterprise Kubernetes Operator 1.17.0

## Improvements

* Introduced support for Ops Manager 6.0.
* For custom S3 compatible backends for the Oplog and Snapshot stores, it is now possible to specify the
  `spec.backup.s3OpLogStores[n].s3RegionOverride` and the `spec.backup.s3Stores[n].s3RegionOverride` parameter.
* Improved security by introducing `readOnlyRootFilesystem` property to all deployed containers. This change also introduces a few additional volumes and volume mounts.
* Improved security by introducing `allowPrivilegeEscalation` set to `false` for all containers.

## Breaking changes and deprecations

* Ubuntu-based images are being deprecated in favor of the UBI-based images for new users, a migration guide for existing users will be published soon.
  The Ubuntu-based images will no longer be made available as of version 1.19. All existing Ubuntu-based images will continue to be
  supported until their version [End Of Life](https://www.mongodb.com/docs/kubernetes-operator/master/reference/support-lifecycle/).
  It is highly recommended to switch into UBI-based images as soon as possible.
* Concatenated PEM format TLS certificates are not supported in Operator 1.17.0 and above. They were deprecated in
  1.13.0. Before upgrading to Operator 1.17.0, please confirm you have upgraded to the `Kubernetes TLS`. Please refer to the
  Migration [Migration Guide](https://www.mongodb.com/docs/kubernetes-operator/v1.16/tutorial/migrate-to-new-tls-format/) before upgrading the Operator.
* Ops Manager 4.4 is [End of Life](https://www.mongodb.com/support-policy/lifecycles) and is no longer supported by the operator. If you're
  using Ops Manager 4.4, please upgrade to a newer version prior to the operator upgrade.

# MongoDB Enterprise Kubernetes Operator 1.16.4

## Security fixes

* The operator and init-ops-manager binaries are built with Go 1.18.4 which addresses security issues.

# MongoDB Enterprise Kubernetes Operator 1.16.3

## MongoDB Resource

* Security Context are now defined only at Pod level (not both Pod and Container level as before).
* Added `timeoutMS`, `userCacheInvalidationInterval` fields to `spec.security.authentication.ldap` object.

* Bug fixes
    * Fixes ignored `additionalMongodConfig.net.tls.mode` for `mongos`, `configSrv` and `shard` objects when configuring ShardedCluster resource.

# MongoDB Enterprise Kubernetes Operator 1.16.2

## MongoDB Resource

* `spec.podSpec.podAntiAffinityTopologyKey` , `spec.podSpec.podAffinity` and `spec.podSpec.nodeAffinity` has been removed. Please use `spec.podSpec.podTemplate` override to set these fields.
* Wiredtiger cache computation has been removed. This was needed for server version `>=4.0.0 <4.0.9` and `<3.6.13`. These server version have reached EOL. Make sure to update your MDB deployment to a version later than `4.0.9` before upgrading the operator.

## MongoDBOpsManager Resource

* `spec.applicationDatabase.podSpec.podAntiAffinityTopologyKey` , `spec.applicationDatabase.podSpec.podAffinity` and `spec.applicationDatabase.podSpec.nodeAffinity` has been removed. Please use `spec.applicationDatabase.podSpec.podTemplate` override to set these fields.
# MongoDB Enterprise Kubernetes Operator 1.16.1

## MongoDB Resource

* `spec.Service` has been deprecated. Please use `spec.statefulSet.spec.serviceName` to provide a custom service name.

# MongoDB Enterprise Kubernetes Operator 1.15.3

## MongoDB Resource

* `spec.security.tls.secretRef.name` has been removed. It was deprecated in operator version `v1.10.0`. Please use the field
  `spec.security.certsSecretPrefix` to specify the secret name containing the certificate for Database. Make sure to create the secret containing the certificates accordingly.
* `spec.podSpec.cpu` and `spec.podSpec.memory` has been removed to override the CPU/Memory resources for the
  database pod, you need to override them using the statefulset spec override under `spec.podSpec.podTemplate.spec.containers`.
* Custom labels specified under `metadata.labels` is propagated to the database StatefulSet and the PVC objects.

## MongoDBOpsManager Resource
* `spec.applicationDatabase.security.tls.secretRef.name` has been removed. It was deprecated in operator version `v1.10.0`. Please use the field
  `spec.applicationDatabase.security.certsSecretPrefix` to specify the secret name containing the certificate for AppDB. Make sure to create the secret containing the certificates accordingly.
* * Custom labels specified under `metadata.labels` is propagated to the OM, AppDB and BackupDaemon StatefulSet and the PVC objects.

## MongoDBUser Resource
* Changes:
    * Added the optional field `spec.connectionStringSecretName` to be able to provide a deterministic secret name for the user specific connection string secret generated by the operator.

* `spec.applicationDatabase.podSpec.cpu` and `spec.applicationDatabase.podSpec.memory` has been removed to override the CPU/Memory resources for the
  appDB pod, you need to override them using the statefulset spec override under `spec.applicationDatabase.podSpec.podTemplate.spec.containers`.
# MongoDB Enterprise Kubernetes Operator 1.15.2
## MongoDBOpsManager Resource
* Bug Fix
    * For enabling custom TLS certificates for S3 Oplog and Snapshot stores for backup. In additioning to setting `spec.security.tls.ca` and `spec.security.tls.secretRef`. The field `spec.backup.s3OpLogStores[n].customCertificate` / `spec.backup.s3Stores[n].customCertificate` needs to be set `true`.

# MongoDB Enterprise Kubernetes Operator 1.15.1

## MongoDBOpsManager Resource

* Bug fixes
    * Fixes an issue that prevented the Operator to be upgraded when managing a TLS
      enabled ApplicationDB, when the ApplicationDB TLS certificate is stored in a
      `Secret` of type Opaque.

# MongoDB Enterprise Kubernetes Operator 1.15.0


## MongoDB Resource

* Changes:
    * The `spec.security.tls.enabled` and `spec.security.tls.secretRef.prefix` fields are now **deprecated** and will be removed in a future release. To enable TLS it is now sufficient to set the `spec.security.certsSecretPrefix` field.

## MongoDBOpsManager Resource

* Changes:
    * A new field has been added: `spec.backup.queryableBackupSecretRef`. The secrets referenced by this field contains the certificates used to enable [Queryable Backups](https://docs.opsmanager.mongodb.com/current/tutorial/query-backup/) feature.
    * Added support for configuring custom TLS certificates for the S3 Oplog and Snapshot Stores for backup. These can be configured with
      `spec.security.tls.ca` and `spec.security.tls.secretRef`.
    * It is possible to disable AppDB processes via the `spec.applicationDatabase.automationConfig.processes[n].disabled` field, this enables backing up the AppDB.
    * The `spec.security.tls.enabled`, `spec.security.tls.secretRef.prefix`, `spec.applicationDatabase.security.tls.enabled` and `spec.applicationDatabase.security.tls.prefix` fields are now **deprecated** and will be removed in a future release. To enable TLS it is now sufficient to set the `spec.security.certsSecretPrefix` and/or `spec.applicationDatabase.security.certsSecretPrefix` field.

*All the images can be found in:*

https://quay.io/repository/mongodb (ubuntu-based)

https://connect.redhat.com/ (rhel-based)


# MongoDB Enterprise Kubernetes Operator 1.14.0


## MongoDB Resource
* Changes:
    * A new field has been added: `spec.backup.autoTerminateOnDeletion`. AutoTerminateOnDeletion indicates if the Operator should stop and terminate the Backup before the cleanup, when the MongoDB Resource is deleted.
* Bug fixes
    * Fixes an issue which would make a ShardedCluster Resource fail when disabling authentication.

## MongoDBOpsManager Resource

* Bug Fixes
    * Fixes an issue where the operator would not properly trigger a reconciliation when rotating the AppDB TLS Certificate.
    * Fixes an issue where a custom CA specified in the MongoDBOpsManager resource was not mounted into the Backup Daemon pod,
      which prevented backups from working when Ops Manager was configured in hybrid mode and used a custom CA.
* Changes
    * Added support for configuring S3 Oplog Stores using the `spec.backup.s3OpLogStores` field.


# MongoDB Enterprise Kubernetes Operator 1.13.0

## Kubernetes Operator
* Breaking Changes:
    * The Operator no longer generates certificates for TLS resources.
* When deploying to multiple namespaces, imagePullSecrets has to be created only in the namespace where the Operator is installed. From here, the Operator will be sync this secret across all watched namespaces.
* The credentials secret used by the Operator now accepts the pair of fields `publicKey` and `privateKey`. These should be preferred to the existent `user` and `publicApiKey` when using Programmatic API Keys in Ops Manager.
* For TLS-enabled resources, the operator now watches the ConfigMap containing the Certificate Authority and the secret containg the TLS certificate. Changes to these resources now trigger a reconciliation of the related resource.
* The Operator can now watch over a list of Namespaces. To install the Operator in this mode, you need to set the value `operator.watchNamespace` to a comma-separated list of Namespaces.
  The Helm install process will create Roles and Service Accounts required, in the Namespaces that the Operator will be watching.

### Support for TLS certificates provided as kubernetes.io/tls secrets
* The operator now supports referencing TLS secrets of type kubernetes.io/tls
    * This type of secrets contain a tls.crt and tls.key entry
    * The operator can read these secrets and automatically generate a new one, containing the concatenation of tls.crt and tls.key
    * This removes the need for a manual concatenation of the fields and enables users to natively reference secrets generated by tools such as cert-manager

**Deprecation Notice**
The usage of generic secrets, manually created by concatenating certificate and private key, is now deprecated.

## MongoDB Resource
* Breaking Changes:
    * The field `spec.project` has been removed from MongoDB spec, this field has been deprecated since operator version `1.3.0`. Make sure to specify the project configmap name under `spec.opsManager.configMapRef.name` or ``spec.cloudManager.configMapRef.name`` before upgrading the operator.
* Changes:
    * A new field has been added: `spec.security.certsSecretPrefix`. This string is now used to determine the name of the secrets containing various TLS certificates:
        * For TLS member certificates, the secret name is `<spec.security.certsSecretPrefix>-<resource-name>-cert`
            * Note: If either `spec.security.tls.secretRef.name` or `spec.security.tls.secretRef.prefix` are specified, these will take precedence over the new field
            * Note: if none of these three fields are specified, the secret name is `<resource-name>-cert`
        * For agent certificates, if `spec.security.certsSecretPrefix` is specified, the secret name is`<spec.security.certsSecretPrefix>-<resource-name>-agent-certs`
            * Note: if `spec.authentication.agents.clientCertificateSecretRef` is specified, this will take precedence over the new field
            * If none of these fields are set, the secret name is still `agent-certs`
        * For internal cluster authentication certificates, if `spec.security.certsSecretPrefix` is specified, the secret name is `<spec.security.certsSecretPrefix>-<resource-name>-clusterfile`
            * Otherwise, it is still `<resource-name>-clusterfile`
* Bug fixes
    * Fixes an issue where Sharded Cluster backups could not be correctly configured using the MongoDB CR.
    * Fixes an issue where Backup Daemon fails to start after OpsManager version upgrade.


## MongoDBOpsManager Resource
* Operator will report status of FileSystemSnaphot store names configured under `spec.backup.fileSystemStores` in OM CR. The FS however needs to be manually configured.
* It is now possible to disable creation of "LoadBalancer" Type service for queryable backup by setting `spec.backup.externalServiceEnabled` to `false` in OM CR. By default, the operator would create the LoadBalancer type service object.
* The operator will now automatically upgrade the used API Key to a programmatic one when deploying OM >= 5.0.0. It is now possible to upgrade from older versions of OM to OM 5.0 without manual intervention.
* A new field has been added: `spec.security.certSecretPrefix`. This is string is now used to determine the name of the secret containing the TLS certificate for OpsManager.
    * If the existing field `spec.security.tls.secretRef.Name` is specified, it will take the precedence
        * Please note that this field is now **deprecated** and will be removed in a future release
    * Otherwise, if `spec.security.certSecretPrefix` is specified, the secret name will be `<spec.security.certSecretPrefix>-<om-resource-name>-cert`

## MongoDBUser Resource
* Breaking Changes:
    * The field `spec.project` has been removed from User spec, this field has been deprecated since operator version `1.3.0`. Make sure to specify the MongoDB resource name under `spec.MongoDBResourceRef.name` before upgrading the operator.
## Miscellaneous
* Ops Manager versions 4.4.7, 4.4.9, 4.4.10, 4.4.11, 4.4.12 and 4.4.13 base images have been updated to Ubuntu 20.04.
* Ops Manager versions 4.4.16 and 5.0.1 are now supported


# MongoDB Enterprise Kubernetes Operator 1.12.0

## MongoDB Resource
* Bug Fixes
    * Fixes a bug when an user could only specify `net.ssl.mode` and not `net.tls.mode` in the `spec.additionalMongodConfig` field.
* Changes
    * If `spec.exposedExternally` is set to `false` after being set to `true`, the Operator will now delete the corresponding service

## MongoDBOpsManager Resource
* Changes
    * If `spec.externalConnectivity` is unset after being set, the Operator will now delete the corresponding service
    * It is now possible to specify the number of backup daemon pods to deploy through the `spec.backup.members` field. The value defaults to 1 if not set.


## Miscellaneous
* Ops Manager versions 4.4.13, 4.4.14, 4.4.15 and 4.2.25 are now supported
* Ops Manager version 5.0.0 is now supported
* Ubuntu based operator images are now based on Ubuntu 20.04 instead of Ubuntu 16.04
* Ubuntu based database images starting from 2.0.1 will be based on Ubuntu 18.04 instead of Ubuntu 16.04
  **NOTE: MongoDB 4.0.0 does not support Ubuntu 18.04 - If you want to use MongoDB 4.0.0, stay on previously released images**
* Ubuntu based Ops Manager images after 4.4.13 will be based on Ubuntu 20.04 instead of Ubuntu 16.04

* Newly released ubi images for Operator, Ops Manager and Database will be based un ubi-minimal instead of ubi
## Notes before upgrading to OpsManager 5.0.0
* Before upgrading OpsManager to version 5.0.0 make sure the Operator is using a [programmatic API key](https://docs.mongodb.com/kubernetes-operator/stable/tutorial/create-operator-credentials/#create-k8s-credentials). This is only required when the OpsManager instance is managed by the Operator.
* You will find a small tutorial on how to do this in [upgrading-to-ops-manager-5.md](docs/upgrading-to-ops-manager-5.md) document.

# MongoDB Enterprise Kubernetes Operator 1.11.0

## MongoDB Resource
* Bug fixes
    * Fixes an issue with the `LivenessProbe` that could cause the database Pods to be restarted in the middle of a restore operation from Backup.

## MongoDBOpsManager Resource
* Breaking Changes
  *For a complete guide on how to safely upgrade, please check the [upgrade instructions](link here TODO)*
    * Each Application Database pod consists now of three containers (`mongodb`, `mongodb-agent`, `mongodb-agent-monitoring`) and it does not bundle anymore a MongoDB version
    * You can now use any version of MongoDB for the Application Database (we recommend to use the enterprise ones provided by MongoDB, see the *New Images* section)
        * You need to make sure the MongoDB version used is [supported](https://docs.opsmanager.mongodb.com/current/reference/mongodb-compatibility/) by OpsManager
    * `spec.applicationDatabase.version` is no longer optional.
    * `spec.applicationDatabase.persistent` does not exist anymore, the Operator will now always use persistent volumes for the AppDB.

## New Images

* mongodb-agent 10.29.0.6830-1:
* Ubi: quay.io/mongodb/mongodb-agent-ubi:10.29.0.6830-1
* Ubuntu: quay.io/mongodb/mongodb-agent:10.29.0.6830-1

* mongodb-enterprise-appdb-database
* Ubi: quay.io/mongodb/mongodb-enterprise-appdb-database-ubi
* Ubuntu: quay.io/mongodb/mongodb-enterprise-appdb-database

* mongodb-enterprise-init-appdb 1.0.7
    * Ubi: quay.io/mongodb/mongodb-enterprise-init-appdb-ubi:1.0.7
    * Ubuntu: quay.io/mongodb/mongodb-enterprise-init-appdb:1.0.7

* mongodb-enterprise-init-database 1.0.3
    * Ubi: quay.io/mongodb/mongodb-enterprise-init-database-ubi:1.0.3
    * Ubuntu: quay.io/mongodb/mongodb-enterprise-init-database:1.0.3

# MongoDB Enterprise Kubernetes Operator 1.10.1

## Kubernetes Operator
* Changes
    * Added a liveness probe to the Backup Daemon.
    * Added a readiness probe to the Backup Daemon.
    * The readiness probe on Database Pods is more strict when restarting a
      Replica Set and will only set the Pod as "Ready" when the MongoDB server has
      reached `PRIMARY` or `SECONDARY` states.

## MongoDB Resource
* Changes
    * Deprecated field `spec.security.tls.secretRef.name`, the field `spec.security.tls.secretRef.prefix` should now be used instead.
    * Added field `spec.security.tls.secretRef.prefix`. This property should be used to specify the prefix of the secret which contains custom tls certificates.

## MongoDBOpsManager Resource
* Changes
    * A new status field for the OpsManager backup has been added: `Disabled`. This status will be displayed when `spec.backup.enabled` is set to `false` and no backup is configured in OpsManager

## Miscellaneous
* Added a new value in openshift-values.yaml `operator_image_name` which allows the label selector of the webhook
  to match the operator label.


## MongoDB Resource
* Changes
    * Deprecated field `spec.security.tls.secretRef.name`, the field `spec.security.tls.secretRef.prefix` should now be used instead.
    * Added field `spec.security.tls.secretRef.prefix`. This property should be used to specify the prefix of the secret which contains custom tls certificates.


# MongoDB Enterprise Kubernetes Operator 1.10.0

## Kubernetes Operator

* Changes
    * The CRDs have been updated to from `v1beta1` to `v1` version. This should not have any impact on Kubernetes clusters 1.16 and up. The CRDs won't be installable in clusters with versions older than 1.16.

* Bug fixes
    * Fixes an issue which made it not possible do have multiple ops-manager resources with the same name in different namespaces.
    * Fixes an issue which made new MongoDB resources created with `spec.backup.mode=disabled` fail.
    * Fixes an issue which made a Replica Set go to Fail state if, at the same time, the amount of members of a Replica Set are increased and TLS is disabled.

## MongoDBOpsManager Resource

* Known issues
    * When using remote or hybrid mode, and `automation.versions.download.baseUrl` has been set, the property `automation.versions.download.baseUrl.allowOnlyAvailableBuilds`
      needs to be set to `false`. This has been fixed in Ops Manager version 4.4.11.


# MongoDB Enterprise Kubernetes Operator 1.9.3
## Kubernetes Operator

* Changes
    * The CRDs have been updated to from `v1beta1` to `v1` version. This should not have any impact on Kubernetes clusters 1.16 and up. The CRDs won't be installable in clusters with versions older than 1.16.

* Bug fixes
    * Fixes an issue which made it not possible do have multiple ops-manager resources with the same name in different namespaces.
    * Fixes an issue which made new MongoDB resources created with `spec.backup.mode=disabled` fail.
    * Fixes an issue which made a Replica Set go to Fail state if, at the same time, the amount of members of a Replica Set are increased and TLS is disabled.

## MongoDBOpsManager Resource

* Known issues
    * When using remote or hybrid mode, and `automation.versions.download.baseUrl` has been set, the property `automation.versions.download.baseUrl.allowOnlyAvailableBuilds`
      needs to be set to `false`. This has been fixed in Ops Manager version 4.4.11.


# MongoDB Enterprise Kubernetes Operator 1.9.3
## Kubernetes Operator
* Bug fixes
    * Fixes an issue which made it not possible do have multiple ops-manager resources with the same name in different namespaces
    * Fixes an issue which made new MongoDB resources created with `spec.backup.mode=disabled` fail
    * Fixes an issue which made a Replica Set go to Fail state if, at the same time, the amount of members of a Replica Set are increased and TLS is disabled.

## MongoDBOpsManager Resource
* Known issues
    * When using remote or hybrid mode, and `automation.versions.download.baseUrl` has been set, the property `automation.versions.download.baseUrl.allowOnlyAvailableBuilds`
      needs to be set to `false`. This has been fixed in Ops Manager version 4.4.11.


# MongoDB Enterprise Kubernetes Operator 1.9.2
## Miscellaneous
* Fix errors with CSV



# MongoDB Enterprise Kubernetes Operator 1.9.1
## Kubernetes Operator
* Bug fixes
    * Fixes an issue where the service-account-name could not be specified in the StatefulSet podSpec override.
    * Removed unnecessary `delete service` permission from operator role.

## MongoDB Resource
* Bug fixes
    * Fixes an issue where updating a role in `spec.security.authentication.roles` by removing the `privileges` array would cause the resource to enter a bad state

## MongoDBOpsManager Resource
* Breaking Changes
    * The new Application Database image `mongodb-enterprise-appdb:10.2.15.5958-1_4.2.11-ent` was released. The image needs
      to be downloaded to the local repositories otherwise MongoDBOpsManager resource won't start. The image contains a new bundled MongoDB
      `4.2.11-ent` instead of `4.2.2-ent`.
* Changes
    * Ops Manager user now has "backup", "restore" and "hostManager" roles, allowing for backups/restores on the AppDB.
    * If `spec.applicationDatabase.version` is omitted the Operator will use `4.2.11-ent` as a default MongoDB.

# MongoDB Enterprise Kubernetes Operator 1.9.0

## Kubernetes Operator

* Bug fixes
    * Fixes an issue where connections were not closed leading to too many file
      descriptors open.

## MongoDB Resource
* Changes
    * Continuous backups can now be configured with the MongoDB CRD. Set `spec.backup.enabled` to `true`. *Note*: You must have an Ops Manager resource already configured with backup. See [the docs](https://docs.mongodb.com/kubernetes-operator/master/tutorial/deploy-om-container/#id6) for more information.
## MongoDBOpsManager Resource

* Changes
    * A StatefulSet resource that holds the Ops Manager Backup Daemon will be
      deleted and recreated in order to change the `matchLabels` attribute,
      required for a new `Service` to allow for Queryable Backups feature to work.
      This is a safe operation.
    * Changed the way the Operator collects statuses of MongoDB Agents running in
      Application Database Pods.

# MongoDB Enterprise Kubernetes Operator 1.8.2

## MongoDBOpsManager Resource

* Bug Fixes
    * Fixes an issue when `MongoDBOpsManager` resource gets to `Failing` state when
      both external connectivity and backups are enabled.

## New Images

* mongodb-enterprise-operator 1.8.2:
* Ubi: quay.io/mongodb/mongodb-enterprise-operator-ubi:1.8.2
* Ubuntu: quay.io/mongodb/mongodb-enterprise-operator:1.8.2
