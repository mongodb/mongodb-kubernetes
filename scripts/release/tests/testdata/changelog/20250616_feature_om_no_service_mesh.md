---
title: OM no Service Mesh support
kind: feature
date: 2025-06-16
---

* **MongoDBOpsManager**, **AppDB**: Introduced support for OpsManager and Application Database deployments across multiple Kubernetes clusters without requiring a Service Mesh.
    * New property [spec.applicationDatabase.externalAccess](TBD) used for common service configuration or in single cluster deployments
    * Added support for existing, but unused property [spec.applicationDatabase.clusterSpecList.externalAccess](TBD)
    * You can define annotations for external services managed by the operator that contain placeholders which will be automatically replaced to the proper values:
        * AppDB: [spec.applicationDatabase.externalAccess.externalService.annotations](TBD)
        * MongoDBOpsManager: Due to different way of configuring external service placeholders are not yet supported
    * More details can be found in the [public documentation](TBD).
