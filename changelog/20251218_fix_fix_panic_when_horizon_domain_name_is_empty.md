---
kind: fix
date: 2025-12-18
---

* Fixed a panic that occurred when the domain names for a horizon was empty. Now, if the domain names are not valid (RFC 1123), the validation will fail before reconciling.
