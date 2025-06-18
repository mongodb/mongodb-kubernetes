# MCK 1.2.0 Release Notes

## New Features

* **MongoDB**, **MongoDBMulti**: Added support for OpenID Connect (OIDC) user authentication.
    * OIDC authentication can be configured with `spec.security.authentication.modes=OIDC` and `spec.security.authentication.oidcProviderConfigs` settings.
    * Minimum MongoDB version requirements:
        * `7.0.11`, `8.0.0`
        * Only supported with MongoDB Enterprise Server
    * For more information please see:
        * [Secure Client Authentication with OIDC](https://www.mongodb.com/docs/kubernetes/upcoming/tutorial/secure-client-connections/)
        * [Manage Database Users using OIDC](https://www.mongodb.com/docs/kubernetes/upcoming/manage-users/)
        * [Authentication and Authorization with OIDC/OAuth 2.0](https://www.mongodb.com/docs/manual/core/oidc/security-oidc/)
