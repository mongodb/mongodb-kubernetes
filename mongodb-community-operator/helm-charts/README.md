# MongoDB Helm Charts repository for Kubernetes

This repository contains Helm Charts for different MongoDB products.

## Supported Charts

The following Charts are supported by MongoDB.

| Charts                                            | Description                                                               |
| ------------------------------------------------- | ------------------------------------------------------------------------- |
| [enterprise-operator](charts/enterprise-operator) | MongoDB Enterprise Kubernetes Operator Helm Chart.                        |
| [enterprise-database](charts/enterprise-database) | MongoDB Enterprise Kubernetes Database Helm Chart.                        |
| [atlas-operator](charts/atlas-operator)           | MongoDB Atlas Operator Helm Chart.                                        |
| [atlas-deployment](charts/atlas-deployment)       | MongoDB Atlas Deployment Helm Chart. Create Mongo Database resources.     |
| [atlas-operator-crds](charts/atlas-operator-crds) | MongoDB Atlas Custom Resource Definitions (CRDs) Helm Chart.              |

## Unsupported Charts

The following Helm Charts are not supported by MongoDB.

| Charts                                            | Description                                                               |
| ------------------------------------------------- | ------------------------------------------------------------------------- |
| [community-operator](charts/community-operator)   | MongoDB Community Operator Helm Chart.                                    |
| [community-operator-crds](charts/community-operator-crds) | MongoDB Community Custom Resource Definitions (CRDs) Helm Chart.  |
| [sample-app](charts/sample-app)                   | A Sample Front/Back-end application backed by a MongoDB Database.         |

- Please note that the `CRD` Charts ([Community](charts/community-operator-crds)
  and [Atlas](charts/atlas-operator-crds)) will be installed, by default,
  as a dependency by the corresponding [Community](charts/community-operator)
  and [Atlas](charts/atlas-operator) Charts.

## Adding the MongoDB Helm Repo

The MongoDB Helm repository can be added using the `helm repo add` command, like
in the following example:

```
$ helm repo add mongodb https://mongodb.github.io/helm-charts
"mongodb" has been added to your repositories
```

## Contribute

Please file issues before filing PRs. For PRs to be accepted, contributors must sign our [CLA](https://www.mongodb.com/legal/contributor-agreement).

Reviewers, please ensure that the CLA has been signed by referring to [the contributors tool](https://contributors.corp.mongodb.com/) (internal link).

## License

These helm charts are available under the Apache 2 license. Please refer to the github repos of the software that the charts install for additional license information.

* [MongoDB Atlas Operator](https://github.com/mongodb/mongodb-atlas-kubernetes)
* [MongoDB Community Kubernetes Operator](https://github.com/mongodb/mongodb-kubernetes-operator)
* [MongoDB Enterprise Kubernetes Operator](https://github.com/mongodb/mongodb-enterprise-kubernetes)