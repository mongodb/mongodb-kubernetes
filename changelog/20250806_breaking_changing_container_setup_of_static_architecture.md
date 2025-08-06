---
title: Changing container setup of static architecture
kind: breaking
date: 2025-08-06
---

This change fixes the current complex and difficult-to-maintain architecture for stateful set containers, which relies on an "agent matrix" to map operator and agent versions which led to a sheer amount of images.
We solve this by shifting to a 3-container setup. This new design eliminates the need for the operator-version/agent-version matrix by adding one additional container containing all required binaries. This architecture maps to what we already do with the mongodb-database container.
