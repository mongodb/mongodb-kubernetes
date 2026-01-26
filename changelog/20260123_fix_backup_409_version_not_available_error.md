---
title: Fix backup failure when redeploying MongoDB with authentication disabled
kind: fix
date: 2026-01-23
---

* Fixed an issue where enabling backup on a MongoDB deployment with authentication disabled could fail with a 409 Conflict error ("MongoDB version information is not yet available") if a previous deployment had authentication enabled.
* The operator now clears stale agent credentials when authentication is disabled, allowing the monitoring agent to connect properly and report version information to Ops Manager.

