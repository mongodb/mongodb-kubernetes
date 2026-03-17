# Sharded MongoDB Search — Query Usage

Reusable scripts for importing data, creating search indexes, and running search queries against a **sharded MongoDB cluster with MongoDB Search**.

These scripts are designed to run **after** a sharded search infrastructure is deployed (e.g., via [`07-search-external-sharded-mongod-managed-lb`](../07-search-external-sharded-mongod-managed-lb/)).

## Prerequisites

The following environment variables must be set before running any script:

| Variable | Description |
|----------|-------------|
| `K8S_CTX` | Kubernetes context name |
| `MDB_NS` | Kubernetes namespace |
| `MDB_VERSION` | MongoDB version (e.g., `8.2.0-ent`) |
| `MDB_ADMIN_CONNECTION_STRING` | Admin connection string (for import/sharding) |
| `MDB_USER_CONNECTION_STRING` | User connection string (for search queries) |
| `MDB_TLS_CA_CONFIGMAP` | CA certificate ConfigMap name (mounted in tools pod) |

These are typically exported by the infrastructure module's `env_variables.sh`.

## Scripts (run in order)

| Script | Purpose |
|--------|---------|
| `08_0410_run_mongodb_tools_pod.sh` | Deploy a `mongodb-tools` pod with mongosh and mongorestore |
| `08_0420_import_sample_data.sh` | Import `sample_mflix` dataset and shard the movies collection |
| `08_0430_create_search_index.sh` | Create a default text search index on movies |
| `08_0435_create_vector_search_index.sh` | Create a vector search index on embedded_movies |
| `08_0440_wait_for_search_indexes.sh` | Wait for search indexes to become READY |
| `08_0450_execute_search_query.sh` | Run a text search query |
| `08_0455_execute_vector_search_query.sh` | Run a vector search query |
