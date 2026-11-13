---
kind: feature
date: 2026-06-08
---

* **Helm Chart**: Added a new `operator.priorityClassName` field that can be used to set the `priorityClassName` of the Operator deployment through the Helm Chart. This allows the Operator pod to be scheduled on clusters where pod priority and preemption are enforced.
