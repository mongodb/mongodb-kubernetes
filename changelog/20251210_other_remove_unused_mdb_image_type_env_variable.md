---
kind: other
date: 2025-12-10
---

* **Operator configuration**: Removed the unused `MDB_IMAGE_TYPE` environment variable and the corresponding `mongodb.imageType` Helm value. This variable was deprecated in v1.28.0 of the MongoDB Enterprise Kubernetes Operator when it switched to architecture-based image selection (ubi9 for static, ubi8 for non-static). This is a cleanup change with no functional impact.
