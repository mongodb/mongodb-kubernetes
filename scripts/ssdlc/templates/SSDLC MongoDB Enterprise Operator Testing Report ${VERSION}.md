MongoDB Enterprise Kubernetes Operator Security Testing Summary
==

This document lists specific instances of security-relevant testing that is being performed for the MongoDB Enterprise Kubernetes Operator. All parts of the MongoDB Enterprise Kubernetes source code are subject to unit and end-to-end testing on every change made to the project, including the specific instances listed below. Additionally, smoke tests (end-to-end) are performed every time we release a new tagged version of Docker images used by the operator.

Authentication End-to-End Tests
===

Our authentication tests verify that multiple authentication mechanisms are supported and configurable for the MongoDB instances deployed by the operator and that changes in the MongoDB custom resources result in correct authentication changes in the underlying automation config. Our tests cover SCRAM, LDAP and X509 authentication. This extends internal cluster authentication. For X509 we also cover certificate rotation.

Vault integration End-to-End Tests
===

Our operator relies on the availability of possibly sensitive data on the customer premise (TLS certificates, database admin passwords, etc.) and the operator provides an integration that allows those secrets to be sourced from Vault. Our end-to-end tests cover this integration to ensure that a customer could run the operator while keeping control of the sensitive data in an external system.

TLS End-to-End Tests
===

Our TLS tests verify that core security properties of TLS connections are applied appropriately for the MongoDB resources. The tests also verify that certificate rotation and database upgrades when TLS is enabled do not introduce downtime in live workloads. These tests cover TLS connections to the mongod servers, OpsManager and the underlying Application Database instances. This means ensuring secure connections within the Kubernetes cluster and secure external connectivity to all the resources deployed in the cluster. The tests also verify that certificate rotation and upgrades when TLS is enabled do not introduce downtime in live workloads.

Static Container Architecture End-to-End Tests
===

All the end-to-end tests that we run for the MongoDB functionality using dynamic containers that pull mongod and agent binaries from OpsManager/CloudManager are replicated in a separate variant for the static architecture to ensure the same functionality is available with this secure model that assures customers that the Docker images they run on-prem do not dynamically change on runtime.

Data encryption End-to-End Tests
===

The enterprise operator supports configuring Automatic Queryable Encryption with KMIP. Our end-to-end tests cover the configuration of MongoDB resources to connect to a KMIP server and encrypt data and related backups.
