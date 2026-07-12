# Sharded MongoDB Search — Query Usage

Use this module to import sample data, build Search/Vector Search indexes, and run query snippets against a sharded MongoDB Search deployment.

Run this query module after one of these infrastructure modules:

- [`07-search-external-sharded-mongod-managed-lb`](../07-search-external-sharded-mongod-managed-lb/)
- [`09-search-sharded-mongod-managed-lb`](../09-search-sharded-mongod-managed-lb/)
- [`13-search-sharded-multi-cluster`](../13-search-sharded-multi-cluster/) (**multi-cluster**)

## Environment handoff

Scenarios 07 and 09 already export the variables required by this module:

```bash
( cd ../08-search-sharded-query-usage && ./test.sh )
```

After scenario 13 reports `MongoDBSearch` in `Running`, run this module from the central cluster context:

```bash
export K8S_CTX="${K8S_CTX_0}"
# Keep MDB_ADMIN_CONNECTION_STRING and MDB_USER_CONNECTION_STRING
# from scenario 13 env_variables.sh

( cd ../08-search-sharded-query-usage && ./test.sh )
```

## Required variable contract

| Variable | Description |
|---|---|
| `K8S_CTX` | Kubernetes context where the tools pod runs |
| `MDB_NS` | Namespace containing MongoDBSearch resources |
| `MDB_VERSION` | MongoDB version used for tools pod image |
| `MDB_ADMIN_CONNECTION_STRING` | Admin connection string for import/sharding operations |
| `MDB_USER_CONNECTION_STRING` | User connection string for query snippets |
| `MDB_CONNECTION_STRING` (fallback) | Used if admin/user strings are not provided |
| `MDB_TLS_CA_CONFIGMAP` | CA ConfigMap mounted into the tools pod (`/tls/ca.crt`) |

## Quick run

```bash
cd docs/search/08-search-sharded-query-usage
./test.sh
```

## Step-by-step snippets

1. `08_0410_run_mongodb_tools_pod.sh` — deploy tools pod.
2. `08_0420_import_sample_data.sh` — import sample data and shard collections.
3. `08_0430_create_search_index.sh` — create text search index.
4. `08_0435_create_vector_search_index.sh` — create vector index.
5. `08_0440_wait_for_search_indexes.sh` — wait for index readiness.
6. `08_0450_execute_search_query.sh` — run text query example.
7. `08_0455_execute_vector_search_query.sh` — run vector query example.
