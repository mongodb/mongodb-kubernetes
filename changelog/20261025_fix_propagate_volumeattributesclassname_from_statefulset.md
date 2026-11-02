---
kind: fix
date: 2026-10-25
---

* **MongoDB**, **MongoDBCommunity**, **MongoDBOpsManager**: Fixed a bug where `volumeAttributesClassName` set in a `volumeClaimTemplates` entry of a `statefulSet` override was silently dropped, so the resulting PersistentVolumeClaims had no VolumeAttributesClass. A change to this field on an existing StatefulSet is now also detected as a change to an immutable field. As with other `statefulSet` overrides, this is an advanced setting: MongoDB doesn't test VolumeAttributesClass with specific storage drivers, and choosing and validating the class for your storage is your responsibility.
