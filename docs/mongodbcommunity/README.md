# MongoDB Controllers for Kubernetes (MCK) - Community #

<img align="right" src="https://mongodb-kubernetes-operator.s3.amazonaws.com/img/Leaf-Forest%402x.png">

This directory and readme cover use of the MongoDB Controllers for Kubernetes (MCK) Operator being used for running MongoDB Community into Kubernetes clusters.

If you are a MongoDB Enterprise customer and need Enterprise features such as Backup, see the [official documentation](https://www.mongodb.com/docs/kubernetes/current/) .

## Documentation

See the documentation to learn how to:

- [Install or upgrade](https://www.mongodb.com/docs/kubernetes/current/) the Operator
- [Supported Features](#supported-features)
- [Contribute](contributing.md)
- [MongoDB Community Kubernetes Operator Architecture](architecture.md)
- [Configure Logging](logging.md) of the MongoDB resource components.
- [Configure Logging of the MongoDB components](logging.md)
- [Create a database user](users.md) with SCRAM authentication.~~
- [Secure MongoDB resource connections](secure.md) using TLS.

## Supported Features

The Operator support of MongoDB Community Edition includes the following:

- Create [replica sets](https://www.mongodb.com/docs/manual/replication/)
- Upgrade and downgrade MongoDB server version
- Scale replica sets up and down
- Read from and write to the replica set while scaling, upgrading, and downgrading. These operations are done in an "always up" manner.
- Report MongoDB server state via the [MongoDBCommunity resource](/config/crd/bases/mongodbcommunity.mongodb.com_mongodbcommunity.yaml) `status` field
- Connect to the replica set from inside the Kubernetes cluster (no external connectivity)
- Secure client-to-server and server-to-server connections with TLS
- Create users with [SCRAM](https://www.mongodb.com/docs/manual/core/security-scram/) authentication
- Create custom roles
- Enable a [metrics target that can be used with Prometheus](prometheus/README.md)

## Linting

This project uses the following linters upon every Pull Request:

* `gosec` is a tool that find security problems in the code
* `Black` is a tool that verifies if Python code is properly formatted
* `MyPy` is a Static Type Checker for Python
* `Kube-linter` is a tool that verified if all Kubernetes YAML manifests are formatted correctly
* `Go vet` A built-in Go static checker
* `Snyk` The vulnerability scanner

## Table of Contents
