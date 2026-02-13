---
title: Fix CLOUDP-68873 auth transition deadlock
kind: fix
date: 2026-02-13
---

* Fixed a race condition during authentication transitions where agents could see an intermediate automation config state with `DeploymentAuthMechanisms` populated but `auth.Disabled=true` and `AutoAuthMechanisms` empty, causing agents to get stuck during auth transition. The fix ensures that mechanism addition to `DeploymentAuthMechanisms`, agent credential creation, and auth enablement happen atomically in a single automation config update for all authentication mechanisms (SCRAM, X509, LDAP).

