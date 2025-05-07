[![Documentation](https://img.shields.io/badge/Documentation-MongoDB-green)](https://www.mongodb.com/docs/kubernetes/current/)

# Mongodb Controllers for Kubernetes (MCK)

## What is MCK
Mongodb Controllers for Kubernetes (MCK) is a Kubernetes operator that enables users to run MongoDB in Kubernetes.

It supports both MongoDB Community and MongoDB Enterprise Advanced.

For **MongoDB Enterprise Advanced**, it supports:
* Manages MongoDB Enterprise Advanced deployments in Kubernetes.
* Integrates with MongoDB Ops Manager or Cloud Manager for advanced monitoring, backups, and automation.
* Supports all MongoDB topologies: replica sets, standalone, and sharded clusters.
  For a full list of capabilities, see the [official documentation](https://www.mongodb.com/docs/kubernetes-operator/current/).

For **MongoDB Community**, it supports:
* Manages MongoDB Community Server [replica sets](https://www.mongodb.com/docs/manual/replication/)
* No integration with Ops Manager or Cloud Manager
* Create & manage database users, leveraging [SCRAM authentication](https://www.mongodb.com/docs/manual/core/security-scram/)
* Create & manage custom roles
* Integrate [with Prometheus](https://github.com/mongodb/mongodb-kubernetes/blob/master/docs/mongodbcommunity/prometheus/README.md)

See [more info](#how-does-mck-differ-from-the-mongodb-enterprise-kubernetes-operator-or-mongodb-community-operator) below for guidance about how MCK differs from other MongoDB Operators, and how to migrate to MCK.

## Getting Started & Further Documentation
* For guidance on using the Operator for Enterprise Advanced, please refer to our official [documentation](https://www.mongodb.com/docs/kubernetes/current/).
*For guidance on using the Operator for MongoDB Community Edition, please refer to the [guidance in this repository](https://github.com/mongodb/mongodb-kubernetes/tree/master/mongodb-community-operator).

## License
Customers with contracts that allowed use of the Enterprise Operator will still be able to leverage the new replacement, allowing customers to adopt it without contract changes. The Operator itself is licensed under the Apache 2.0, and a license file [included in the repository](LICENSE-MCK) provides further details. License entitlements for all other MongoDB products and tools remain unchanged (for example Enterprise Server and Ops Manager) - if in doubt, contact your MongoDB account team.

## Support, Feature Requests and Community
MCK is supported by the [MongoDB Support Team](https://support.mongodb.com/). If you need help, please file a support ticket. If you have a feature request, you can make one on our [Feedback Site](https://feedback.mongodb.com/forums/924355-ops-tools)

You can discuss this integration in our new [Community Forum](https://developer.mongodb.com/community/forums/) - please use the tag [kubernetes-operator](https://developer.mongodb.com/community/forums/tag/kubernetes-operator)

## How does MCK differ from the MongoDB Enterprise Kubernetes Operator or MongoDB Community Operator?
MCK unifies MongoDB's support for running MongoDB in Kubernetes into a single Operator.

This unifies:
* [MongoDB Enterprise Kubernetes Operator](https://www.mongodb.com/docs/kubernetes-operator/current/)
* [MongoDB Community Operator](https://github.com/mongodb/mongodb-kubernetes-operator)

While early versions of MCK simply bring the capabilities of both previous Operators into a single new Operator, future changes will build on this to more closely align how Community and Enterprise are managed in Kubernetes to offer an even more seamless and streamlined experience.

As an open-source project, MCK allows for community contributions, helping drive quicker bug fixes and ongoing innovation.

### Deprecation and EOL for MCO and MEKO
* [MongoDB Community Operator](https://github.com/mongodb/mongodb-kubernetes-operator) End-of-Life (EOL): We will continue best efforts support for 6 months (until November, 2025)
* [MongoDB Enterprise Kubernetes Operator](https://www.mongodb.com/docs/kubernetes-operator/current/) End-of-Life (EOL): No change to the [current EOL](https://www.mongodb.com/docs/kubernetes-operator/current/reference/support-lifecycle/) for each individual MEKO version.

No impact on current contracts or agreements.

### Migration
Migration from [MongoDB Community Operator](https://github.com/mongodb/mongodb-kubernetes-operator) and [MongoDB Enterprise Kubernetes Operator](https://www.mongodb.com/docs/kubernetes-operator/current/) to MCK is seamless: your MongoDB deployments are not impacted by the upgrade and require no changes. Simply follow the upgrade instructions provided in the MCK documentation. See our [migration guidance](https://dochub.mongodb.org/core/migrate-to-mck).
See our detailed migration guides:
- [Migrating from MongoDB Community Operator](docs/migration/community-operator-migration.md)
- [Migrating from MongoDB Enterprise Kubernetes Operator](https://www.mongodb.com/docs/kubernetes/current/tutorial/migrate-to-mck/)

