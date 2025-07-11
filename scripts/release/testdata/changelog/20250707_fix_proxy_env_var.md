---
title: Fixed handling proxy environment variables in the operator pod
kind: fix
date: 2025-07-07
---

* Fixed handling proxy environment variables in the operator pod. The environment variables [`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`] when set on the operator pod, can now be propagated to the MongoDB agents by also setting the environment variable `MDB_PROPAGATE_PROXY_ENV=true`.
