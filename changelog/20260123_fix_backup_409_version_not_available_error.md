---
title: Fix backup failure when redeploying MongoDB with authentication disabled
kind: fix
date: 2026-01-23
---

* Fixed an issue where the monitoring agent failed to report version information to Ops Manager when a MongoDB deployment with authentication disabled was created after a previous deployment with authentication enabled had been deleted.
* The operator now clears stale agent credentials from monitoring and backup agent configs when authentication is disabled, preventing authentication failures against MongoDB instances that have auth disabled.
