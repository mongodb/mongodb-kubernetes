---
kind: other
date: 2025-12-10
---

* **Operator configuration**: Removed the unused `MDB_IMAGE_TYPE` environment variable and the corresponding `mongodb.imageType` Helm value. This variable was originally used by the community operator but became orphaned after the MEKO/MCO merge. The community operator now uses `MDB_COMMUNITY_IMAGE_TYPE` instead. This is a cleanup change with no functional impact.
