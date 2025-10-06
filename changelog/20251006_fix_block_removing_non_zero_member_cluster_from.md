---
kind: fix
date: 2025-10-06
---

* **MultiClusterSharded**: Block removing non-zero member cluster from MongoDB resource. This prevents from scaling down member cluster without current configuration available, which can lead to unexpected issues. Previously operator was crashing in that scenario, after the fix it will mark reconciliation as `Failed` with appropriate message. Example unsafe scenario that is now blocked:
  * User has 2 member clusters: `main` is used for application traffic, `read-analytics` is used for read-only analytics
  * `main` cluster has 7 voting members
  * `read-analytics` cluster has 3 non-voting members
  * User decides to remove `read-analytics` cluster, by removing the `clusterSpecItem` completely
  * Operator scales down members from `read-analytics` cluster one by one
  * Because the configuration does not have voting options specified anymore and by default `priority` is set to 1, the operator will remove one member, but the other two members will be reconfigured as voting members
  * `replicaset` contains now 9 voting members, which is not [supported by MongoDB](https://www.mongodb.com/docs/manual/reference/limits/#mongodb-limit-Number-of-Voting-Members-of-a-Replica-Set)
