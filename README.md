# Mongodb Controllers for Kubernetes (MCK)

## What is MCK

MongoDB is unifying its Kubernetes offerings with the introduction of MongoDB Controllers for Kubernetes (MCK). This new operator is an open-source project and represents a merge of the previous MongoDB Community Operator (MCO) and the MongoDB Enterprise Kubernetes Operator (MEKO).

This brings MongoDB Community and Enterprise editions together under a single, unified operator, making it easier to manage, scale, and upgrade your deployments. While the first version simply brings the capabilities of both into a single Operator, future changes will build on this to more closely align how Community and Enterprise are managed in Kubernetes, to offer an even more seamless and streamlined experience. As an open-source project, it now allows for community contributions, helping drive quicker bug fixes and ongoing innovation.

MCK enables easily deploying the following applications in Kubernetes:
* MongoDB - Replica Sets, Sharded Clusters and Standalones - with authentication, TLS and many more options.
* Ops Manager - our enterprise management, monitoring and backup platform for MongoDB. The Operator can install and manage Ops Manager in Kubernetes for you. Ops Manager can manage MongoDB instances both inside and outside Kubernetes.
This includes running MongoDB clusters or Ops Manager across multiple Kubernetes clusters for higher resilience.

The Operator requires access to one of our database management tools - Ops Manager or Cloud Manager - to deploy MongoDB instances. You may run Ops Manager either inside or outside Kubernetes, or may use Cloud Manager (cloud.mongodb.com) instead.

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

## Getting Started & Further Documentation
* For more information on how to install and deploy the operator as well as workloads, please refer to our official [documentation](https://www.mongodb.com/docs/kubernetes/current/).

## License

Customers with contracts that allowed use of the Enterprise Operator will still be able to leverage the new replacement, allowing customers to adopt it without contract changes. The Operator itself is licensed under the Apache 2.0, and a license file [included in the repository](#) provides further detail. License entitlements for all other MongoDB products and tools remain unchanged (for example Enterprise Server and Ops Manager) \- if in doubt, contact your MongoDB account team.

## Support, Feature Requests and Community
MCK is supported by the [MongoDB Support Team](https://support.mongodb.com/). If you need help, please file a support ticket. If you have a feature request, you can make one on our [Feedback Site](https://feedback.mongodb.com/forums/924355-ops-tools)

You can discuss this integration in our new [Community Forum](https://developer.mongodb.com/community/forums/) - please use the tag [kubernetes-operator](https://developer.mongodb.com/community/forums/tag/kubernetes-operator)

## Migration

Migration from MCO and MEKO to MCK is seamless: your MongoDB deployments are not impacted by the upgrade and require no changes. Simply follow the upgrade instructions provided in the MCK documentation. See our [migration guidance](https://dochub.mongodb.org/core/migrate-to-mck).

## Deprecation and EOL for MCO and MEKO

We will continue best efforts support of MCO for 6 months (until November, 2025), and versions of MEKO will remain supported according to the current [current EOL](https://www.mongodb.com/docs/kubernetes-operator/current/reference/support-lifecycle/) guidance. All future bug fixes and improvements will be released in new versions of MCK. We encourage all users to plan their migration to MCK within these timelines.

[![Documentation](https://img.shields.io/badge/Documentation-MongoDB-green)](https://www.mongodb.com/docs/kubernetes/current/)
