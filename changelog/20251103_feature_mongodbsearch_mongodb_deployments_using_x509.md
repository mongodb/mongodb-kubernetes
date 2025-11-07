---
kind: feature
date: 2025-11-03
---

* **MongoDBSearch**: MongoDB deployments using X509 internal cluster authentication are now supported. Previously MongoDB Search required SCRAM authentication among members of a MongoDB replica set. Note: SCRAM client authentication is still required, this change merely relaxes the requirements on internal cluster authentication.

