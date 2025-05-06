# Mongodb Controllers for Kubernetes (MCK)

**What is MCK**

MongoDB is unifying its Kubernetes offerings with the introduction of MongoDB Controllers for Kubernetes (MCK). This new operator is an open-source project and represents a merge of the previous MongoDB Community Operator (MCO) and the MongoDB Enterprise Kubernetes Operator (MEKO).

This release brings MongoDB Community and Enterprise editions together under a single, unified operator, making it easier to manage, scale, and upgrade your deployments. While the first version simply brings the capabilities of both into a single Operator, future changes will build on this to more closely align how Community and Enterprise are managed in Kubernetes, to offer an even more seamless and streamlined experience. As an open-source project, it now allows for community contributions, helping drive quicker bug fixes and ongoing innovation.

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

**Getting Started & Further Documentation**
* for more information on how to install and deploy the operator as well as workloads see in our official [documentation](https://www.mongodb.com/docs/kubernetes/current/).

**License**

Customers with contracts that allowed use of the Enterprise Operator will still be able to leverage the new replacement, allowing customers to adopt it without contract changes. The Operator itself is licensed under the Apache 2.0, and a license file [included in the repository](#) provides further detail. License entitlements for all other MongoDB products and tools remain unchanged (for example Enterprise Server and Ops Manager) \- if in doubt, contact your MongoDB account team.

**Migration**

Migration from MCO and MEKO to MCK is seamless: your MongoDB deployments are not impacted by the upgrade and require no changes. Simply follow the upgrade instructions provided in the MCK documentation. See our [migration guidance](https://dochub.mongodb.org/core/migrate-to-mck).

**Deprecation and EOL for MCO and MEKO**

We will continue best efforts support of MCO for 6 months (until November, 2025), and versions of MEKO will remain supported according to the current [current EOL](https://www.mongodb.com/docs/kubernetes-operator/current/reference/support-lifecycle/) guidance. All future bug fixes and improvements will be released in new versions of MCK. We encourage all users to plan their migration to MCK within these timelines.

[![Documentation](https://img.shields.io/badge/Documentation-MongoDB-green)](https://www.mongodb.com/docs/kubernetes/current/)
