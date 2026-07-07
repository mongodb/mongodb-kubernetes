---
kind: fix
date: 2026-07-07
---

* **MongoDBSearch**: The default JVM heap size (half of the memory request) is now capped at 30GB, following the [mongot sizing guidance](https://www.mongodb.com/docs/manual/tutorial/mongot-sizing/advanced-guidance/hardware/#jvm-heap-sizing). Heap sizes above ~30GB prevent the JVM from using compressed object pointers and degrade performance. User-provided heap flags are not affected.
