*(Please use the [release template](docs/dev/release/release-notes-template.md) as the template for this document)*
<!-- Next Release -->
# MongoDB Enterprise Kubernetes Operator 1.11.1

## MongoDB Resource
* Bug Fixes
  * If `spec.exposedExternally` is set to `false` after being set to `true`, the Operator will now delete the corresponding service

## MongoDBOpsManager Resource
* Ops Manager versions 4.4.13 and 4.2.25 are now supported.

## Miscellaneous
Ubuntu-based images are now based on Ubuntu 20.04 instead of Ubuntu 16.06

<!-- Past Releases -->
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
