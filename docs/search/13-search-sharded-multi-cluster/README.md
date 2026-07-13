# Multi-cluster MongoDB Search with a sharded cluster

This scenario deploys a multi-cluster `MongoDBSearch` resource for an existing
Ops Manager-managed sharded cluster. The shell snippets do not create or
reconfigure the source MongoDB deployment.

## Prerequisites

- A MongoDB 8.2 or later sharded cluster managed by Ops Manager, with `mongos`
  and shard members reachable from both Kubernetes clusters.
- Two Kubernetes contexts connected by a service mesh that provides
  cross-cluster Service DNS and connectivity.
- `kubectl`, Helm, the `kubectl-mongodb` plugin, `jq`, and cert-manager.
- A `search-sync-source` user on the source deployment with the
  `searchCoordinator` role. Set `MDB_SEARCH_SYNC_USER_PASSWORD` to that user's
  password.
- A cert-manager `ClusterIssuer` named by `MDB_TLS_CA_ISSUER` on the central
  cluster. It must issue certificates trusted by the source MongoDB deployment.
- A ConfigMap named by `MDB_TLS_CA_CONFIGMAP` in `MDB_NS` on both clusters. It
  must contain the source deployment's CA certificate under the `ca-pem` and
  `ca.crt` keys.

## Configure the source deployment in Ops Manager

The source `mongos` and shard `mongod` processes must route Search traffic to
cluster 0's managed proxies.

1. Edit `env_variables.sh`, then load and validate it:

   ```bash
   source env_variables.sh
   bash code_snippets/13_0040_validate_env.sh
   ```

   `MDB_EXTERNAL_CLUSTER_NAME` defaults to `mdb-mc-sh`. Change it to the
   existing deployment name. Also set the existing shard names and every
   `MDB_EXTERNAL_*_HOST_*` endpoint to match the source deployment.

2. In Ops Manager, open **Deployment > Processes** and click **Modify** for
   each process listed below.
3. Under **Advanced Configuration Options**, click **Add Option**, select
   `setParameter`, and add `mongotHost` and
   `searchIndexManagementHostAndPort` with these values:

   | Process | Value for both parameters |
   |---------|---------------------------|
   | Every `mongos` | `${MDB_PROXY_HOST_0}` |
   | Every shard 0 `mongod` | `${MDB_PROXY_HOST_SHARD_0}` |
   | Every shard 1 `mongod` | `${MDB_PROXY_HOST_SHARD_1}` |
   | Every shard 2 `mongod` | `${MDB_PROXY_HOST_SHARD_2}` |

   Enter the expanded values from `env_variables.sh`, not the variable names.
4. Click **Save**, review the changes, and deploy the updated configuration.
   Do not add these Search parameters to config server processes.

Ops Manager applies `setParameter` startup options to the processes it manages.
See [Advanced Options for MongoDB Deployments](https://www.mongodb.com/docs/ops-manager/current/reference/deployment-advanced-options/).

Processes use cluster 0's proxies because the source deployment has one shared
value per process role. Processes in cluster 1 therefore send Search traffic
across the service mesh to cluster 0.

## Deploy MongoDB Search

Run only the customer steps listed below:

```bash
bash code_snippets/13_0045_create_namespaces.sh
bash code_snippets/13_0100_install_operator.sh
bash code_snippets/13_0301_install_cert_manager.sh
bash code_snippets/13_0316_create_search_sync_secret.sh
bash code_snippets/13_0316a_create_mongot_tls_certificates.sh
bash code_snippets/13_0316b_create_lb_tls_certificates.sh
bash code_snippets/13_0317_replicate_search_secrets.sh
bash code_snippets/13_0320_create_mongodb_search_resource.sh
bash code_snippets/13_0325_wait_for_search_resource.sh
bash code_snippets/13_0330_show_running_pods.sh
```

The password Secret is created on cluster 0 and then replicated with the TLS
Secrets to cluster 1. The `MongoDBSearch` resource uses the `mongos` and shard
hostnames from `env_variables.sh`.

## Run Search queries

Use the shared sharded-cluster query snippets; a separate multi-cluster query
is not required because applications connect through `mongos` normally. Set
both optional connection strings in `env_variables.sh` before running the query
snippets:

- `MDB_ADMIN_CONNECTION_STRING` must be authorized to restore data, enable
  sharding, create indexes, and shard collections in `sample_mflix`.
- `MDB_USER_CONNECTION_STRING` must have `readWrite` access to `sample_mflix`,
  including permission to create and manage Search indexes.

```bash
export K8S_CTX="${K8S_CTX_0}"

( cd ../08-search-sharded-query-usage && ./test.sh )
```
