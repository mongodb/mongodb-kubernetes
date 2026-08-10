---
kind: breaking
date: 2026-07-24
---

* Remove Webhook installClusterRole Helm Value

This Helm value controls whether the ClusterRole for webhook registration is deployed, defaulting to `true`.
In MCK 2.x this is redundant — we instead rely on `operator.webhook.registerConfiguration`.
