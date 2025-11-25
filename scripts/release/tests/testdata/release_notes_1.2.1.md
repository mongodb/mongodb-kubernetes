# MCK 1.2.1 Release Notes

## Bug Fixes

* Fixed a bug where workloads in the `static` container architecture were still downloading binaries. This occurred when the operator was running with the default container architecture set to `non-static`, but the workload was deployed with the `static` architecture using the `mongodb.com/v1.architecture: "static"` annotation.
* **MongoDB**: Operator now correctly applies the external service customization based on `spec.externalAccess` and `spec.mongos.clusterSpecList.externalAccess` configuration. Previously it was ignored, but only for Multi Cluster Sharded Clusters.
