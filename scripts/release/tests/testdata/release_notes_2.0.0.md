# MCK 2.0.0 Release Notes

This change is making `static` architecture a default and deprecates the `non-static` architecture.

## Breaking Changes

* **MongoDB**, **MongoDBMulti**: Static architecture is now the default for MongoDB and MongoDBMulti resources.

## New Features

* **MongoDBOpsManager**, **AppDB**: Introduced support for OpsManager and Application Database deployments across multiple Kubernetes clusters without requiring a Service Mesh.
    * New property [spec.applicationDatabase.externalAccess](TBD) used for common service configuration or in single cluster deployments
    * Added support for existing, but unused property [spec.applicationDatabase.clusterSpecList.externalAccess](TBD)
    * You can define annotations for external services managed by the operator that contain placeholders which will be automatically replaced to the proper values:
        * AppDB: [spec.applicationDatabase.externalAccess.externalService.annotations](TBD)
        * MongoDBOpsManager: Due to different way of configuring external service placeholders are not yet supported
    * More details can be found in the [public documentation](TBD).

## Bug Fixes

* Fixed a bug where workloads in the `static` container architecture were still downloading binaries. This occurred when the operator was running with the default container architecture set to `non-static`, but the workload was deployed with the `static` architecture using the `mongodb.com/v1.architecture: "static"` annotation.
* **MongoDB**: Operator now correctly applies the external service customization based on `spec.externalAccess` and `spec.mongos.clusterSpecList.externalAccess` configuration. Previously it was ignored, but only for Multi Cluster Sharded Clusters.
