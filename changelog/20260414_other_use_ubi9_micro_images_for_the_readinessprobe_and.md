---
kind: other
date: 2026-04-14
---

* **Container images**: Use ubi9-micro images for the readinessprobe and upgrade hook containers to reduce the attack surface. the ubi mirco images have a much smaller package list and, given that these images are only used to copy a binary into a temporary volume for the main container to use, the micro images are sufficient.
