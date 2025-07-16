---
title: Undocumented operator.enablePVCResize Helm value has been removed
kind: other
date: 2025-07-15
---

* The undocumented `operator.enablePVCResize` Helm value has been removed. If you previously set this value to `false`, please note that the operator roles will now include permissions for `PersistentVolumeClaim` resources by default.
