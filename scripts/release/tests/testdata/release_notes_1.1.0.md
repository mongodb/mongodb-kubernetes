# MCK 1.1.0 Release Notes

## New Features

* **MongoDBSearch (Community Private Preview)**: Added support for deploying MongoDB Search (Community Private Preview Edition) that enables full-text and vector search capabilities for MongoDBCommunity deployments.
    * Added new MongoDB CRD which is watched by default by the operator.
        * For more information please see: [docs/community-search/quick-start/README.md](docs/community-search/quick-start/README.md)
    * Private Preview phase comes with some limitations:
        * minimum MongoDB Community version: 8.0.
        * TLS must be disabled in MongoDB (communication between mongot and mongod is in plaintext for now).
