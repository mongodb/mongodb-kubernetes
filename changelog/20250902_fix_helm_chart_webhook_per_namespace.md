---
title: helm chart - webhook per namespace
kind: fix
date: 2025-09-02
---

* Changed webhook ClusterRole and ClusterRoleBinding default names to include the namespace. This ensures that multiple operator installations in different namespaces don't conflict with each other.
