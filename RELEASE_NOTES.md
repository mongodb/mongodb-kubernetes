[//]: # (Consider renaming or removing the header for next release, otherwise it appears as duplicate in the published release, e.g: https://github.com/mongodb/mongodb-enterprise-kubernetes/releases/tag/1.22.0 )
<!-- Next Release -->

# MCK 1.2.0 Release Notes

## New Features

* Added new **ClusterMongoDBRole** CRD to support reusable roles across multiple MongoDB clusters.
  * This allows users to define roles once and reuse them in multiple **MongoDB** or **MongoDBMultiCluster** resources. The role can be referenced through the `.spec.security.roleRefs` field. Note that only one of `.spec.security.roles` and `.spec.security.roleRefs` can be used at a time.
  * **ClusterMongoDBRole** resources are treated by the operator as a custom role templates that are only used when referenced by the database resources.
  * The new resource is watched by default by the operator. This means that the operator will require a new **ClusterRole** and **ClusterRoleBinding** to be created in the cluster. **ClusterRole** and **ClusterRoleBinding** resources are created by default with the helm chart or the kubectl mongodb plugin.
    * To disable this behavior in the helm chart, set the `operator.enableClusterMongoDBRoles` value to `false`. This will disable the creation of the necessary RBAC resources for the **ClusterMongoDBRole** resource, as well as disable the watch for this resource.
    * To not install the necessary **ClusterRole** and **ClusterRoleBinding** with the kubectl mongodb plugin set the `--create-mongodb-roles-cluster-role` to false.
  * The new **ClusterMongoDBRole** resource is designed to be read-only, meaning it can be used by MongoDB deployments managed by different operators.
  * The **ClusterMongoDBRole** resource can be deleted at any time, but the operator will not delete any roles that were created using this resource. To properly remove access, you must **manually** remove the reference to the **ClusterMongoDBRole** in the **MongoDB** or **MongoDBMultiCluster** resources.
  * The reference documentation for this resource can be found here: **TODO** (link to documentation)
  * For more information please see: **TODO** (link to documentation)
* **MongoDB**, **MongoDBMulti**: Added support for OpenID Connect (OIDC) user authentication.
  * OIDC authentication can be configured with `spec.security.authentication.modes=OIDC` and `spec.security.authentication.oidcProviderConfigs` settings.
  * Minimum MongoDB version requirements:
    * `7.0.0`, `8.0.0`
    * Only supported with MongoDB Enterprise Server
  * For more information please see:
    * [Authentication Settings](https://www.mongodb.com/docs/kubernetes/current/reference/k8s-operator-specification/#mongodb-setting-spec.security.authentication.modes)
    * [Authentication and Authorization with OIDC/OAuth 2.0](https://www.mongodb.com/docs/manual/core/oidc/security-oidc/)

<!-- Past Releases -->

# MCK 1.1.0 Release Notes

## New Features

* **MongoDBSearch (Community Private Preview)**: Added support for deploying MongoDB Search (Community Private Preview Edition) that enables full-text and vector search capabilities for MongoDBCommunity deployments.
  * Added new MongoDB CRD which is watched by default by the operator.
    * For more information please see: [docs/community-search/quick-start/README.md](docs/community-search/quick-start/README.md)
  * Private Preview phase comes with some limitations:
    * minimum MongoDB Community version: 8.0.
    * TLS must be disabled in MongoDB (communication between mongot and mongod is in plaintext for now).

# MCK 1.0.1 Release Notes

## Bug Fixes
* Fix missing agent images in the operator bundle in OpenShift catalog and operatorhub.io.
* **MongoDBCommunity** resource was missing from watched list in Helm Charts

# MCK 1.0.0 Release Notes

Exciting news for MongoDB on Kubernetes\! We're happy to announce the first release of MongoDB Controllers for Kubernetes (MCK), a unified open-source operator merging our support of MongoDB Community and Enterprise in Kubernetes.

**Acronyms**

* **MCK:** MongoDB Controllers for Kubernetes
* **MCO:** MongoDB Community Operator
* **MEKO:** MongoDB Enterprise Kubernetes Operator

**TL;DR:**

* MCK: A unified MongoDB Kubernetes Operator, merging MCO and MEKO.
* This initial release provides the combined functionality of the latest MCO and MEKO so migration is seamless: no changes are required in your current deployments.
* No impact on current contracts or agreements.
* We are adopting Semantic Versioning (SemVer), so any future breaking changes will only occur in new major versions of the Operator.
* MCO End-of-Life (EOL): Support for MCO is best efforts, with no formal EOL for each version. For the last version of MCO, we will continue to offer best efforts guidance, but there will be no further releases.
* MEKO End-of-Life (EOL): No change to the [current EOL](https://www.mongodb.com/docs/kubernetes-operator/current/reference/support-lifecycle/) for each individual MEKO version.

**About the First MCK Release**

MongoDB is unifying its Kubernetes offerings with the introduction of MongoDB Controllers for Kubernetes (MCK). This new operator is an open-source project and represents a merge of the previous MongoDB Community Operator (MCO) and the MongoDB Enterprise Kubernetes Operator (MEKO).

This release brings MongoDB Community and Enterprise editions together under a single, unified operator, making it easier to manage, scale, and upgrade your deployments. While the first version simply brings the capabilities of both into a single Operator, future changes will build on this to more closely align how Community and Enterprise are managed in Kubernetes, to offer an even more seamless and streamlined experience. As an open-source project, it now allows for community contributions, helping drive quicker bug fixes and ongoing innovation.

**License**

Customers with contracts that allowed use of the Enterprise Operator will still be able to leverage the new replacement, allowing customers to adopt it without contract changes. The Operator itself is licensed under the Apache 2.0, and a license file [included in the repository](#) provides further detail. License entitlements for all other MongoDB products and tools remain unchanged (for example Enterprise Server and Ops Manager) \- if in doubt, contact your MongoDB account team.

**Migration**

Migration from MCO and MEKO to MCK is seamless: your MongoDB deployments are not impacted by the upgrade and require no changes. Simply follow the upgrade instructions provided in the MCK documentation. See our [migration guidance](https://dochub.mongodb.org/core/migrate-to-mck).

**Deprecation and EOL for MCO and MEKO**

We will continue best efforts support of MCO for 6 months (until November, 2025), and versions of MEKO will remain supported according to the current [current EOL](https://www.mongodb.com/docs/kubernetes-operator/current/reference/support-lifecycle/) guidance. All future bug fixes and improvements will be released in new versions of MCK. We encourage all users to plan their migration to MCK within these timelines.
