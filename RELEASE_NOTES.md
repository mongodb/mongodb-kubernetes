*(Please use the [release template](docs/dev/release-notes-template.md) as the template for this document)* 
<!-- Next release -->
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

<!-- Past Releases -->
# MongoDB Enterprise Kubernetes Operator 1.8.2

## MongoDBOpsManager Resource

* Bug Fixes
  * Fixes an issue when `MongoDBOpsManager` resource gets to `Failing` state when
   both external connectivity and backups are enabled.

## New Images

* mongodb-enterprise-operator 1.8.2:
 * Ubi: quay.io/mongodb/mongodb-enterprise-operator-ubi:1.8.2
 * Ubuntu: quay.io/mongodb/mongodb-enterprise-operator:1.8.2
