---
kind: feature
date: 2026-10-30
---

* **MongoDBOpsManager**: Setting `-XX:MaxRAMPercentage` in `spec.jvmParameters` or `spec.backup.jvmParameters` now sizes the Ops Manager or Backup Daemon JVM heap as a percentage of the container's memory limit, so the heap follows the pod when it's resized. Previously the operator's default heap overrode it. The default of 90% of the container's memory is unchanged. When the Ops Manager container sets `OM_AUTO_HEAP=true`, the operator no longer overrides the heap that Ops Manager picks. Ops Manager's auto heap never goes below 8096 MiB, so only use it on pods with enough memory.
