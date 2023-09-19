*(Please use the [release template](docs/dev/release/release-notes-template.md) as the template for this document)*
<!-- Next Release -->
# MongoDB Enterprise Kubernetes Operator 1.22.0
## Breaking Changes
* The "Reconciling" state is no longer used by the Operator. In most of the cases it has been replaced with "Pending" and a proper message

## Deprecations
## Bug Fixes
## New Features
* An Automatic Recovery mechanism has been introduced for `MongoDB` resources and is turned on by default. If a Custom Resource remains in `Pending` or `Failed` state for a longer period of time (controlled by `MDB_AUTOMATIC_RECOVERY_BACKOFF_TIME_S` environment variable at the Operator Pod spec level, the default is 20 minutes)
  the Automation Config is pushed to the Ops Manager. This helps to prevent a deadlock when an Automation Config can not be pushed because of the StatefulSet not being ready and the StatefulSet being not ready because of a broken Automation Config.
  The behaviour can be turned off by setting `MDB_AUTOMATIC_RECOVERY_ENABLE` environment variable to `false`.

###  MongoDBOpsManager Resource
## New Features
* Improved handling of unreachable clusters in AppDB Multi-Cluster resources:
  * The operator will still successfully manage the remaining healthy clusters, as long as they have a majority of votes to elect a primary.
  * Unreachable clusters specified in both `mongodb-enterprise-operator-member-list` and `spec.applicationDatabase.clusterSpecList` will be bypassed during the resource reconciliation.
  * The associated processes of an unreachable cluster are not automatically removed from the automation config and replica set configuration. These processes will only be removed under the following conditions:
    * The corresponding cluster is deleted from `spec.applicationDatabase.clusterSpecList` or has zero members specified. 
    * When deleted, the operator scales down the replica set by removing processes tied to that cluster one at a time.
* Add support for configuring [logRotate](https://www.mongodb.com/docs/ops-manager/current/reference/cluster-configuration/#mongodb-instances) on the automation-agent for appdb.
* [systemLog](https://www.mongodb.com/docs/manual/reference/configuration-options/#systemlog-options) can now be configured to differ from the otherwise default of `/var/log/mongodb-mms-automation`.

<!-- Past Releases -->
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

<!-- Past Releases -->
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
