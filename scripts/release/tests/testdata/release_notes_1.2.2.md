# MCK 1.2.2 Release Notes

## Bug Fixes

* **MongoDB**: Fixed placeholder name for `mongos` in Single Cluster Sharded with External Domain set. Previously it was called `mongodProcessDomain` and `mongodProcessFQDN` now they're called `mongosProcessDomain` and `mongosProcessFQDN`.
* **MongoDB**, **MongoDBMultiCluster**, **MongoDBOpsManager**: In case of losing one of the member clusters we no longer emit validation errors if the failed cluster still exists in the `clusterSpecList`. This allows easier reconfiguration of the deployments as part of disaster recovery procedure.
