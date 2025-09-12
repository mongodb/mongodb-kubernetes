---
title: Improved agent certificate rotation
kind: feature
date: 2025-09-09
---

* Database Pods now mount a secret containing both old and new certificates, with file names being the hash of the certificate. When it is time to rotate the certificate, the operator updates the automation config with a new path (including the hash) to the certificate. This eliminates the need to manually restart Pods during certificate rotation.
