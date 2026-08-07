# Multi-cluster MongoDB Search with a replica set

This scenario deploys a multi-cluster `MongoDBSearch` resource for an existing
Ops Manager-managed replica set. The shell snippets do not create or
reconfigure the source MongoDB deployment.

## Prerequisites

- A MongoDB 8.2 or later replica set managed by Ops Manager. MongoDB 8.3 or
  later is recommended. Members must be reachable from both Kubernetes clusters.
- Two Kubernetes contexts connected by a service mesh that provides
  cross-cluster Service DNS and connectivity.
- The namespace snippets use Istio's injection label. Adjust that label when
  using another service mesh.
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

The source `mongod` processes must route Search traffic to cluster 0's managed
proxy.

1. Edit `env_variables.sh`, then load and validate it:

   ```bash
   source env_variables.sh
   bash code_snippets/12_0040_validate_env.sh
   ```

   `MDB_EXTERNAL_CLUSTER_NAME` defaults to `mdb-mc-rs`. Change it to the
   existing replica-set name, and set each `MDB_EXTERNAL_HOST_*` value to an
   endpoint that the Kubernetes clusters can reach.

2. In Ops Manager, open **Deployment > Processes** and click **Modify** for
   each `mongod` in the replica set.
3. Under **Advanced Configuration Options**, click **Add Option**, select
   `setParameter`, and add these parameters:

   | Parameter | Value |
   |-----------|-------|
   | `mongotHost` | `${MDB_PROXY_HOST_0}` |
   | `searchIndexManagementHostAndPort` | `${MDB_PROXY_HOST_0}` |
   | `searchTLSMode` | `requireTLS` |
   | `useGrpcForSearch` | `true` |
   | `skipAuthenticationToMongot` | `false` |
   | `skipAuthenticationToSearchIndexManagementServer` | `false` |

   Enter the expanded values from `env_variables.sh`, not the variable names.
4. Click **Save**, review the changes, and deploy the updated configuration.

Ops Manager applies `setParameter` startup options to the processes it manages.
See [Advanced Options for MongoDB Deployments](https://www.mongodb.com/docs/ops-manager/current/reference/deployment-advanced-options/).

These snippets deliberately use one stable Search target: all replica-set
members send Search traffic across the service mesh to cluster 0's proxy.
Cluster 1's Search stack is a warm standby. Ops Manager's per-process
configuration can instead target each member at its local cluster proxy.

## Deploy MongoDB Search

Run only the customer steps listed below:

```bash
bash code_snippets/12_0045_create_namespaces.sh
bash code_snippets/12_0100_install_operator.sh
bash code_snippets/12_0301_install_cert_manager.sh
bash code_snippets/12_0316_create_search_sync_secret.sh
bash code_snippets/12_0316a_create_mongot_tls_certificates.sh
bash code_snippets/12_0316b_create_lb_tls_certificates.sh
bash code_snippets/12_0317_replicate_search_secrets.sh
bash code_snippets/12_0320_create_mongodb_search_resource.sh
bash code_snippets/12_0325_wait_for_search_resource.sh
bash code_snippets/12_0330_show_running_pods.sh
```

The password Secret is created on cluster 0 and then replicated with the TLS
Secrets to cluster 1. The `MongoDBSearch` resource uses the source replica-set
hostnames from `env_variables.sh`.

## Run Search queries

Use the shared replica-set query snippets; a separate multi-cluster query is
not required because applications connect to MongoDB normally. Set
the optional `MDB_CONNECTION_STRING` in `env_variables.sh` to a user that can
restore data and has `readWrite` access to `sample_mflix`, including permission
to create and manage Search indexes.

```bash
export K8S_CTX="${K8S_CTX_0}"
( cd ../03-search-query-usage && ./test.sh )
```
