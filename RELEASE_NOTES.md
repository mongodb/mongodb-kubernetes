*(Please use the [release template](docs/dev/release/release-notes-template.md) as the template for this document)*
<!-- Next release -->
# MongoDB Enterprise Kubernetes Operator 1.9.2
## Kubernetes Operator
* Bug fixes
  * Fixes an issue which made it not possible do have multiple ops-manager resources with the same name in different namespaces
## Miscellaneous
* Fix errors with CSV

<!-- Past Releases -->

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
