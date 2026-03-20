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
| `08_0410_run_mongodb_tools_pod.sh` | Deploy a `mongodb-tools` pod for running database commands. This pod provides `mongosh`, `mongorestore`, and other MongoDB tools for importing data and running queries against the cluster. The pod mounts the CA certificate ConfigMap at `/tls` for TLS connections. |
| `08_0420_import_sample_data.sh` | Import the `sample_mflix` dataset and shard the `movies` collection using a hashed `_id` key. |
| `08_0430_create_search_index.sh` | Create a default text search index on the `movies` collection. Search indexes are created through `mongos` and automatically distributed to each shard's `mongot` instance through the Envoy proxy. This creates a default text search index that indexes all text fields. |
| `08_0435_create_vector_search_index.sh` | Create a vector search index on the `embedded_movies` collection. Vector search indexes enable similarity search on vector embeddings. The `sample_mflix` dataset includes pre-computed embeddings in `embedded_movies`. |
| `08_0440_wait_for_search_indexes.sh` | Wait for search indexes to become READY. After creating search indexes, `mongot` needs time to build them. Polls using `runCommand({listSearchIndexes: ...})` until status is READY. |
| `08_0450_execute_search_query.sh` | Execute a text search query through `mongos`. |
| `08_0455_execute_vector_search_query.sh` | Execute a vector search query. Vector search finds documents similar to a given vector embedding. Uses a sample embedding to find similar movies. |
