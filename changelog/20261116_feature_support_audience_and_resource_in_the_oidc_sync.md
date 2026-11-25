---
title: Support audience and resource in the OIDC sync-source config
kind: feature
date: 2026-11-16
---

* Added optional `audience` and `resource` fields to the `MongoDBSearch` OIDC sync-source configuration (`spec.source.oidc`). Both are sent as token-request parameters, which some identity providers require: Auth0 and Okta need `audience` to mint a token with an `aud` claim, and Entra ID v1-style providers need `resource` (RFC 8707). MongoDB accepts exactly one audience value in the token, so without these fields those providers cannot be used.
