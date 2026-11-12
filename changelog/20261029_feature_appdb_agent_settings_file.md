---
kind: feature
date: 2026-10-29
---

* **MongoDBOpsManager**: AppDB now starts the automation agent with a native settings file, keeping startup options and monitoring credentials out of the command line. AppDB agent startup options are no longer evaluated by a shell. `$(hostname)` is passed to the agent as written, and `$VAR` references only use variables set in the container environment. Upgrading restarts the AppDB pods once.
