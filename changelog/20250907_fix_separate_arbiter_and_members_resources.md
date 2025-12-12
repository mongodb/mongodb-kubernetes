---
title: Separate arbiter and members resources
kind: fix
date: 2025-09-07
---

**Issue:** Previously, arbiter StatefulSets were incorrectly using the same resource specifications as data-bearing members, which could lead to resource over-allocation or under-allocation for arbiters that have different resource requirements.
- Separated resource creation logic for arbiters and data-bearing members
- Implemented a default resource template specifically for arbiter nodes
- Arbiters now use their own StatefulSet configuration instead of inheriting from `spec.statefulSet`
- This ensures arbiters receive appropriate resource allocation based on their lighter workload requirements

