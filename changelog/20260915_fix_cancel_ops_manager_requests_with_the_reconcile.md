---
kind: fix
date: 2026-09-15
---

* Every Ops Manager API request made by the Operator is now bound to the reconcile context. When a reconcile is cancelled or the Operator shuts down, in-flight requests, retries and the wait loops for agent goal state and backup status are aborted instead of running to their own timeouts. For sharded clusters, the wait for mongos, config server and shard agents to register now shares a single deadline instead of one per component.
