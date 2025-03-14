# MongoDB Atlas Operator CRDs Helm Chart

A Helm chart for installing and upgrading Custom Resource Definitions (CRDs) for
the [MongoDB Atlas
Operator](https://github.com/mongodb/mongodb-atlas-kubernetes). These CRDs are
required by the [Atlas Operator](../atlas-operator/) to work.

This Helm chart can be installed manually, following these instructions. If needed, it can
also be installed automatically as a dependency by the [Atlas
Operator](../atlas-operator/).

## Usage

_If you haven't done it yet, [add the MongoDB Helm repository](../README.md)._

Installing the CRDs into the Kubernetes Cluster:

```
helm install atlas-operator-crds mongodb/mongodb-atlas-operator-crds
```

Upgrading the CRDs:

```
helm upgrade atlas-operator-crds mongodb/mongodb-atlas-operator-crds
```
