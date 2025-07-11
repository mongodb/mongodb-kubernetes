---
title: Fix MongoDBUser Phase Bug
kind: fix
date: 2025-07-12
---

* Fixes the bug when status of `MongoDBUser` was being set to `Updated` prematurely. For example, new users were not immediately usable following `MongoDBUser` creation despite the operator reporting `Updated` state.
