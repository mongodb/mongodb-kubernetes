# Replica Set MongoDB Search — Query Usage

Use this module to import sample data, create Search/Vector Search indexes, and run query snippets against a replica-set MongoDB Search deployment.

Run this query module after one of these infrastructure modules:

- [`01-search-community-deploy`](../01-search-community-deploy/)
- [`10-search-external-rs-mongod-managed-lb`](../10-search-external-rs-mongod-managed-lb/)
- [`11-search-rs-mongod-managed-lb`](../11-search-rs-mongod-managed-lb/)
- [`12-search-rs-multi-cluster`](../12-search-rs-multi-cluster/) (**multi-cluster**)

## Multi-cluster handoff (Scenario 12)

After scenario 12 reports `MongoDBSearch` in `Running`, run this module from the central cluster context:

```bash
export K8S_CTX="${K8S_CTX_0}"
export MDB_CONNECTION_STRING="${MDB_USER_CONNECTION_STRING}"

( cd ../03-search-query-usage && ./test.sh )
```

## Required variable contract

| Variable | Description |
|---|---|
| `K8S_CTX` | Kubernetes context where the tools pod runs |
| `MDB_NS` | Namespace containing MongoDBSearch resources |
| `MDB_VERSION` | MongoDB version used for tools pod image |
| `MDB_CONNECTION_STRING` | Connection string used for import/index/query snippets |
| `MDB_TLS_CA_CONFIGMAP` | CA ConfigMap mounted into the tools pod (`/tls/ca.crt`) |
| `EMBEDDING_MODEL` (optional) | When set, auto-embed index/query snippets are also executed |

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
5. `03_0440_wait_for_search_index_ready.sh` — wait for index readiness.
6. `03_0444_list_search_indexes.sh` / `03_0445_list_vector_search_indexes.sh` / `03_0447_list_auto_embed_vector_search_indexes.sh` — inspect index status.
7. `03_0450_execute_search_query.sh` / `03_0455_execute_vector_search_query.sh` / `03_0456_execute_auto_embed_vector_search_query.sh` — run query examples.

Auto-embed snippets (`03_0437`, `03_0447`, `03_0456`) run only when `EMBEDDING_MODEL` is set.
