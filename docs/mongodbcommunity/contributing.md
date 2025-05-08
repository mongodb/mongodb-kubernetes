# Contributing to MongoDB Controllers for Kubernetes (MCK) - Community

First you need to get familiar with the [Architecture guide](architecture.md), which explains
from a high perspective how everything works together.

This strategy is based on using
[`envtest`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/envtest) for setting up the tests
and `go test` for running the tests, and making the test-runner itself run as a Kubernetes Pod. This
makes it easier to run the tests in environments with access to a Kubernetes
cluster with no go toolchain installed locally, making it easier to reproduce
our local working environments in CI/CD systems.

# High-Perspective Architecture

The operator itself consists of 1 image, that has all the operational logic to deploy and
maintain the MongoDBCommunity resources in your cluster.

The operator deploys MongoDB in Pods (via a higher-level resource, a
StatefulSet), on each Pod there will be multiple images coexisting during the
lifetime of the MongoDB server.

* Agent image: This image includes a binary provided by MongoDB that handles
the local operation of a MongoDB server given a series of configurations
provided by the operator. The configuration exists as a ConfigMap that's created
by the operator and mounted in the Agent's Pod.

* MongoDB image: Docker image that includes the MongoDB server.

* Version upgrade post-start hook image: This image includes a binary that helps orchestrate the
  restarts of the MongoDB Replica Set members, in particular, when dealing with
  version upgrades, which requires a very precise set of operations to allow for
  seamless upgrades and downgrades, with no downtime.

Each Pod holds a member of a Replica Set, and each Pod has different components,
each one of them in charge of some part of the lifecycle of the MongoDB database.

# Getting Started

## PR Prerequisites
* Please ensure you have signed our Contributor Agreement. You can find it [here](https://www.mongodb.com/legal/contributor-agreement).

* Please ensure that all commits are signed.
