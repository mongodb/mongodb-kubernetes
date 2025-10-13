---
kind: fix
date: 2025-10-06
---

* **MultiClusterSharded**: Blocked removing non-zero member cluster from MongoDB resource. This prevents from scaling down member cluster without current configuration available, which could lead to unexpected issues.
