# Scenario 10: MongoDB Search with External Replica Set + Managed Envoy Load Balancer

Deploy [MongoDB Search (mongot)](https://www.mongodb.com/docs/atlas/atlas-search/) on Kubernetes against an **existing external MongoDB replica set** using an **operator-managed Envoy L7 proxy** as the load balancer.

## Overview

This scenario is for users who already have a MongoDB replica set running outside the operator's management (e.g. on VMs, another Kubernetes cluster, or a self-hosted deployment) and want to add Atlas Search capabilities via the MongoDB Kubernetes Operator.

**Key characteristic — Managed LB mode:** the operator automatically deploys and configures the Envoy proxy. You do **not** need to create Envoy ConfigMaps, Deployments, or Services yourself.

### Traffic Flow

```
External mongod (your cluster)
        │
        ▼
  Envoy L7 Proxy  ← operator-managed, port 27029
        │
        ▼
   mongot pods     ← operator-managed, port 27028
```

### What the Operator Creates

When you apply the `MongoDBSearch` resource with `lb.mode: Managed`, the operator automatically provisions:

| Resource | Name Pattern | Purpose |
|----------|-------------|---------|
| Deployment | `<name>-search-lb` | Envoy proxy pods |
| ConfigMap | `<name>-search-lb-config` | Envoy routing configuration |
| Service | `<name>-search-lb-svc` | Load balancer service (port 27029) |
| StatefulSet | `<name>-search` | mongot pods |
| Service | `<name>-search-svc` | Headless service for mongot pods |

## Prerequisites

Before starting, ensure you have:

- A running **MongoDB replica set** (v8.2.0+ Enterprise) accessible from your Kubernetes cluster
- A **search-sync-source** user on the external cluster with appropriate permissions for mongot sync
- **kubectl** configured with access to your target Kubernetes cluster
- **Helm 3** installed
- **Network connectivity** from your Kubernetes cluster to the external MongoDB hosts

## Step-by-Step Instructions

### Step 0: Configure Environment Variables

Copy and edit the environment variables file with your deployment settings:

```bash
cp env_variables.sh my_env.sh
# Edit my_env.sh with your values
source my_env.sh
```

Key variables to set:

| Variable | Description | Example |
|----------|-------------|---------|
| `K8S_CTX` | Your Kubernetes context | `my-k8s-cluster` |
| `MDB_NS` | Target namespace | `mongodb` |
| `MDB_EXTERNAL_CLUSTER_NAME` | Identifier for your external RS | `ext-mdb-rs` |
| `MDB_SEARCH_RESOURCE_NAME` | Name for the MongoDBSearch resource | `ext-rs-search` |
| `MDB_RS_MEMBERS` | Number of RS members | `3` |
| `MDB_MONGOT_REPLICAS` | Number of mongot replicas | `2` |
| `MDB_EXTERNAL_HOST_0..N` | Host:port for each RS member | `mongo-0.example.com:27017` |
| `MDB_ADMIN_USER_PASSWORD` | Admin user password | *(change default)* |
| `MDB_USER_PASSWORD` | Application user password | *(change default)* |
| `MDB_SEARCH_SYNC_USER_PASSWORD` | Search sync user password | *(change default)* |

### Step 1: Validate Environment

Verify all required variables are set and the Kubernetes context exists:

```bash
source code_snippets/10_0040_validate_env.sh
```

### Step 2: Create Namespace

```bash
source code_snippets/10_0045_create_namespaces.sh
```

### Step 3: Install the MongoDB Kubernetes Operator

Add the MongoDB Helm repository and install the operator:

```bash
source code_snippets/10_0090_helm_add_mongodb_repo.sh
source code_snippets/10_0100_install_operator.sh
```

The operator pod should reach `Running` state in the target namespace.

### Step 4: Install cert-manager

TLS is required for communication between all components. [cert-manager](https://cert-manager.io/) automates certificate lifecycle:

```bash
source code_snippets/10_0301_install_cert_manager.sh
```

If cert-manager is already installed in your cluster, this step is skipped automatically.

### Step 5: Configure TLS Prerequisites

Create a self-signed CA hierarchy and distribute the CA certificate to the target namespace:

```bash
source code_snippets/10_0302_configure_tls_prerequisites.sh
```

This creates:
1. A **self-signed ClusterIssuer** (bootstrap only)
2. A **CA Certificate** (10-year validity, ECDSA)
3. A **CA ClusterIssuer** (used to sign all subsequent certificates)
4. Distributes the CA as both a **ConfigMap** (`ca-pem` key) and a **Secret** (`ca.crt` key) in the target namespace

> **Note:** If you already have a CA and cert-manager issuer, you can skip this step and update the `MDB_TLS_CA_ISSUER`, `MDB_TLS_CA_CONFIGMAP`, and `MDB_TLS_CA_SECRET_NAME` variables to reference your existing resources.

### Step 6: Create TLS Certificates for mongot

Generate a TLS certificate covering the mongot StatefulSet pods and the LB service:

```bash
source code_snippets/10_0316a_create_mongot_tls_certificates.sh
```

SANs included:
- `*.<name>-search-svc.<namespace>.svc.cluster.local` (mongot pods)
- `<name>-search-lb-svc.<namespace>.svc.cluster.local` (LB service for SNI routing)

### Step 7: Create TLS Certificates for the Load Balancer

The managed Envoy proxy needs a **server certificate** (for incoming mongod connections) and a **client certificate** (for outgoing mongot connections):

```bash
source code_snippets/10_0316b_create_lb_tls_certificates.sh
```

### Step 8: Create the MongoDBSearch Resource

This is the main deployment step. It creates the `MongoDBSearch` custom resource pointing to your external replica set with managed LB mode:

```bash
source code_snippets/10_0320_create_mongodb_search_resource.sh
```

The resource spec looks like:

```yaml
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ext-rs-search
spec:
  replicas: 2                          # number of mongot pods
  source:
    username: search-sync-source
    passwordSecretRef:
      name: ext-rs-search-search-sync-source-password
      key: password
    external:
      hostAndPorts:                     # your external RS members
        - mongo-0.example.com:27017
        - mongo-1.example.com:27017
        - mongo-2.example.com:27017
      tls:
        ca:
          name: root-secret            # CA secret for verifying external RS
  security:
    tls:
      certsSecretPrefix: certs
  lb:
    mode: Managed                       # operator creates Envoy automatically
```

**Important:** Before applying, create the password secret for the search-sync-source user:

```bash
kubectl create secret generic <search-resource-name>-search-sync-source-password \
  --from-literal=password='<your-search-sync-user-password>' \
  -n "${MDB_NS}" --context "${K8S_CTX}"
```

### Step 9: Wait for MongoDBSearch to Reach Running State

```bash
source code_snippets/10_0325_wait_for_search_resource.sh
```

This polls the resource status for up to 10 minutes. The `MongoDBSearch` resource goes through these phases:
1. **Pending** — resource accepted, operator is provisioning
2. **Running** — mongot pods are synced and healthy

### Step 10: Verify Envoy Deployment

Confirm the operator-managed Envoy proxy is running:

```bash
source code_snippets/10_0326_verify_envoy_deployment.sh
```

This checks for:
- Envoy ConfigMap (`<name>-search-lb-config`)
- Envoy Deployment (`<name>-search-lb`) with all replicas ready
- LB Service (`<name>-search-lb-svc`)

### Step 11: Show Running Pods

Get an overview of all deployed resources:

```bash
source code_snippets/10_0330_show_running_pods.sh
```

Expected output includes:
- **mongot pods** (`<name>-search-0`, `<name>-search-1`, ...)
- **Envoy proxy pod(s)** (`<name>-search-lb-*`)
- **Operator pod** (`mongodb-kubernetes-operator-*`)

## TLS Certificate Hierarchy

```
Self-Signed ClusterIssuer
        │
        ▼
   CA Certificate (root-secret)
        │
        ▼
   CA ClusterIssuer (my-ca-issuer)
       ╱ │ ╲
      ▼  ▼  ▼
 mongot  LB   LB      (+ external MongoDB RS cert,
  cert  server client    if managed by cert-manager)
        cert   cert
```

## Code Snippets Reference

| # | Script | Internal | Description |
|---|--------|----------|-------------|
| 1 | `10_0040_validate_env.sh` | No | Validate all required environment variables |
| 2 | `10_0045_create_namespaces.sh` | No | Create Kubernetes namespace |
| 3 | `10_0046_internal_create_image_pull_secrets.sh` | Yes | Create image pull secrets (private registries) |
| 4 | `10_0090_helm_add_mongodb_repo.sh` | No | Add MongoDB Helm repository |
| 5 | `10_0100_install_operator.sh` | No | Install MongoDB Kubernetes Operator |
| 6 | `10_0300_internal_create_ops_manager_resources.sh` | Yes | Create Ops Manager connection resources |
| 7 | `10_0301_install_cert_manager.sh` | No | Install cert-manager |
| 8 | `10_0302_configure_tls_prerequisites.sh` | No | Configure self-signed CA and distribute certs |
| 9 | `10_0304_internal_generate_tls_certificates.sh` | Yes | Generate TLS cert for simulated external RS |
| 10 | `10_0310_internal_create_external_mongodb_rs.sh` | Yes | Create simulated external MongoDB RS |
| 11 | `10_0315_internal_wait_for_external_cluster.sh` | Yes | Wait for simulated RS to be ready |
| 12 | `10_0316_internal_create_external_mongodb_users.sh` | Yes | Create users on simulated RS |
| 13 | `10_0316a_create_mongot_tls_certificates.sh` | No | Create TLS certificate for mongot pods |
| 14 | `10_0316b_create_lb_tls_certificates.sh` | No | Create TLS certificates for Envoy LB |
| 15 | `10_0320_create_mongodb_search_resource.sh` | No | Create MongoDBSearch custom resource |
| 16 | `10_0325_wait_for_search_resource.sh` | No | Wait for MongoDBSearch to reach Running |
| 17 | `10_0326_verify_envoy_deployment.sh` | No | Verify Envoy proxy deployment |
| 18 | `10_0330_show_running_pods.sh` | No | Show all running pods and services |
| 19 | `10_9010_internal_delete_namespace.sh` | Yes | Delete namespace (cleanup) |

Scripts marked **Internal** are used for E2E testing to simulate an external MongoDB cluster. They are not needed when you already have a running external replica set.

## Troubleshooting

### MongoDBSearch stuck in Pending

```bash
kubectl describe mongodbsearch <name> -n <namespace>
kubectl logs -l app.kubernetes.io/component=mongot -n <namespace> --tail=50
```

Common causes:
- Password secret not created for `search-sync-source` user
- TLS certificates not ready (check `kubectl get certificates -n <namespace>`)
- External MongoDB not reachable from the cluster

### Envoy pods not starting

```bash
kubectl describe deployment <name>-search-lb -n <namespace>
kubectl logs -l app=<name>-search-lb -n <namespace>
```

Common causes:
- LB TLS certificates not ready
- Resource limits too restrictive

### mongot cannot connect to external MongoDB

- Verify network connectivity: `kubectl run --rm -it nettest --image=busybox -- nc -zv <host> <port>`
- Check the `search-sync-source` user exists on the external RS with correct permissions
- Verify TLS CA is trusted by both sides

## Cleanup

To remove all resources created in the namespace:

```bash
kubectl delete namespace "${MDB_NS}" --context "${K8S_CTX}"
```
