---
kind: other
date: 2026-06-29
---

* Removed the preliminary scale-down step that set votes and priority to 0 for replica set members before removing them. This step is not necessary anymore since the agent can handle the removal of voting members.
