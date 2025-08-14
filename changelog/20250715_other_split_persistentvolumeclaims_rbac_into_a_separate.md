---
title: Permissions for PersistentVolumeClaim moved to a separate role
kind: other
date: 2025-07-15
---

* Optional permissions for `PersistentVolumeClaim` moved to a separate role. When managing the operator with Helm it is possible to disable permissions for `PersistentVolumeClaim` resources by setting `operator.enablePVCResize` value to `false` (`true` by default). When enabled, previously these permissions were part of the primary operator role. With this change, permissions have a separate role.
