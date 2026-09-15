---
kind: fix
date: 2026-09-10
---

* Bounded the Ops Manager API pagination loop with a maximum page count and an overall deadline, added a request timeout to the Ops Manager HTTP client, and validated that the `baseUrl` read from the project `ConfigMap` is a well-formed `http(s)` URL. A misbehaving or malicious Ops Manager endpoint can no longer stall a reconcile worker indefinitely; the request now fails with a descriptive error instead of blocking, and reconciliation retries or continues.
