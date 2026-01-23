---
title: Handle transient 409 error when starting backup
kind: fix
date: 2026-01-23
---

* Fixed a flaky test issue where enabling backup on a newly created or recreated MongoDB deployment could fail with a 409 Conflict error ("MongoDB version information is not yet available").
* The operator now treats this error as a transient condition and retries instead of immediately marking the deployment as Failed.
* This allows the monitoring agent time to register with Ops Manager before backup configuration is attempted.

