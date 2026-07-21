# Run MongoDB Search Queries on a Replica Set

Use these snippets to import sample data, create Search and Vector Search indexes,
and run queries against a MongoDB Search deployment for a replica set.

Run these query snippets after you complete one of these deployment scenarios:

- [`01-search-community-deploy`](../01-search-community-deploy/)
- [`02-search-enterprise-deploy`](../02-search-enterprise-deploy/)
- [`04-search-external-mongod`](../04-search-external-mongod/)
- [`10-search-external-rs-mongod-managed-lb`](../10-search-external-rs-mongod-managed-lb/)
- [`11-search-rs-mongod-managed-lb`](../11-search-rs-mongod-managed-lb/)
- [`12-search-rs-multi-cluster`](../12-search-rs-multi-cluster/) (**multi-cluster**)

## Environment handoff

Scenario 11 exports separate administrator and user connection strings. Map the
user connection string to `MDB_CONNECTION_STRING`, which these snippets require:

```bash
export MDB_CONNECTION_STRING="${MDB_USER_CONNECTION_STRING}"

( cd ../03-search-query-usage && ./test.sh )
```

After scenario 12 reports `MongoDBSearch` in `Running`, run these query snippets
from the central cluster context:

```bash
export K8S_CTX="${K8S_CTX_0}"
export MDB_CONNECTION_STRING="${MDB_USER_CONNECTION_STRING}"

( cd ../03-search-query-usage && ./test.sh )
```

## Required variables

| Variable | Description |
|---|---|
| `K8S_CTX` | Kubernetes context where the tools pod runs |
| `MDB_NS` | Namespace containing MongoDBSearch resources |
| `MDB_VERSION` | MongoDB version used for tools pod image |
| `MDB_CONNECTION_STRING` | Connection string used to import data, create indexes, and run queries |
| `MDB_TLS_CA_CONFIGMAP` | CA ConfigMap mounted into the tools pod (`/tls/ca.crt`) |
| `EMBEDDING_MODEL` (optional) | Runs the auto-embedding index and query snippets when set |

## Quick run

```bash
cd docs/search/03-search-query-usage
./test.sh
```

## Step-by-step snippets

1. `03_0410_run_mongodb_tools_pod.sh` — deploy tools pod.
2. `03_0420_import_movies_mflix_database.sh` — import sample data.
3. `03_0430_create_search_index.sh` — create text search index.
4. `03_0435_create_vector_search_index.sh` — create vector index.
5. `03_0440_wait_for_search_index_ready.sh` — check the text index status; rerun it until the index reports `READY`.
6. `03_0444_list_search_indexes.sh`, `03_0445_list_vector_search_indexes.sh`, and `03_0447_list_auto_embed_vector_search_indexes.sh` — inspect index status.
7. `03_0450_execute_search_query.sh`, `03_0455_execute_vector_search_query.sh`, and `03_0456_execute_auto_embed_vector_search_query.sh` — run query examples.

Auto-embedding snippets (`03_0437`, `03_0447`, `03_0456`) run only when you set `EMBEDDING_MODEL`.
