# MongoDB Enterprise Search on Kubernetes - Quick Start

This guide provides instructions for deploying MongoDB Enterprise Edition along with its Search capabilities onto a Kubernetes cluster. By following these steps, you will set up a MongoDB instance and configure search indexes to perform full-text search queries against your data.

## Prerequisites

Before you begin, ensure you have the following tools and configurations in place:

- **Kubernetes cluster**: A running Kubernetes cluster (e.g., Minikube, Kind, GKE, EKS, AKS) with kubeconfig available locally.
- **kubectl**: The Kubernetes command-line tool, configured to communicate with your cluster.
- **Helm**: The package manager for Kubernetes, used here to install the MongoDB Kubernetes Operator.
- **Bash 5.1+**: All shell commands in this guide are intended to be run in Bash. Scripts in this guide are automatically tested on Linux with Bash 5.1.
- **Ops Manager or Cloud Manager**: Deploying MongoDB Enterprise Edition requires an Ops Manager or Cloud Manager project and API credentials.

## Setup Steps

The following steps guide you through deploying MongoDB Enterprise with Search. Each step provides a shell script.
**It is important to first source the `env_variables.sh` script provided and customize its values for your environment.**
The subsequent script snippets rely on the environment variables defined in `env_variables.sh`. You should copy and paste each script into your Bash terminal.

### 1. Configure Environment Variables

First, you need to set up your environment. The `env_variables.sh` script, shown below, contains variables for the subsequent steps. You should create this file locally or use the linked one.

Download or copy the content of `env_variables.sh`:
[env_variables.sh](env_variables.sh)
```shell copy
# set it to the context name of the k8s cluster
export K8S_CLUSTER_0_CONTEXT_NAME="<local cluster context>"

# the following namespace will be created if not exists
export MDB_NAMESPACE="mongodb"

# minimum required MongoDB version for running MongoDB Search is 8.0.10
export MDB_VERSION="8.0.10"

# root admin user for restoring the database from a sample backup
export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
# regular user performing search queries on sample mflix database
export MDB_USER_PASSWORD="mdb-user-password-CHANGE-ME"
# user for MongoDB Search to connect to the replica set to synchronise data from
export MDB_SEARCH_SYNC_USER_PASSWORD="search-sync-user-password-CHANGE-ME"

export MDB_OPS_MANAGER_CONFIG_MAP_NAME="<OpsManager project configmap name>"
export MDB_OPS_MANAGER_CREDENTIALS_SECRET_NAME="<OpsManager credentials secret name>"

export OPERATOR_HELM_CHART="mongodb/mongodb-kubernetes"
# comma-separated key=value pairs for additional parameters passed to the helm-chart installing the operator
export OPERATOR_ADDITIONAL_HELM_VALUES=""
```
This will load the variables into your current shell session, making them available for the commands in the following steps.

### 2. Add MongoDB Helm Repository

First, add the MongoDB Helm repository. This repository contains the Helm chart required to install the MongoDB Kubernetes Operator. The operator automates the deployment and management of MongoDB instances (both Community and Enterprise editions) on Kubernetes.

[code_snippets/090_helm_add_mogodb_repo.sh](code_snippets/090_helm_add_mogodb_repo.sh)
```shell copy
helm repo add mongodb https://mongodb.github.io/helm-charts
helm repo update mongodb
helm search repo mongodb/mongodb-kubernetes
```

### 3. Install MongoDB Kubernetes Operator

Next, install the MongoDB Kubernetes Operator from the Helm repository you just added. The Operator will watch for MongoDB and MongoDBSearch custom resources and manage the lifecycle of your MongoDB deployments.

[code_snippets/0100_install_operator.sh](code_snippets/0100_install_operator.sh)
```shell copy
helm upgrade --install --debug --kube-context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --create-namespace \
  --namespace="${MDB_NAMESPACE}" \
  mongodb-kubernetes \
  --set "${OPERATOR_ADDITIONAL_HELM_VALUES:-"dummy=value"}" \
  "${OPERATOR_HELM_CHART}"
```
This command installs the operator in the `mongodb` namespace (creating it if it doesn't exist).

## Creating a MongoDB Search Deployment

With the prerequisites and initial setup complete, you can now deploy MongoDB Enterprise Edition and enable Search.

### 4. Create MongoDB Enterprise Resource

Now, deploy MongoDB Enterprise by creating a `MongoDB` custom resource named `mdb-rs`. This resource definition instructs the MongoDB Kubernetes Operator to configure a MongoDB replica set with 3 members, running version 8.0.10. MongoDB Search is supported only from MongoDB Enterprise Server version 8.0.10. It also defines CPU and memory resources for the `mongodb-enterprise-database` container, and sets up three users:


[code_snippets/0305_create_mongodb_database_resource.sh](code_snippets/0305_create_mongodb_database_resource.sh)
```yaml copy
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: mdb-rs
spec:
  members: 3
  version: ${MDB_VERSION}
  type: ReplicaSet
  opsManager:
    configMapRef:
      name: ${MDB_OPS_MANAGER_CONFIG_MAP_NAME}
  credentials: ${MDB_OPS_MANAGER_CREDENTIALS_SECRET_NAME}
  security:
    authentication:
      enabled: true
      ignoreUnknownUsers: true
      modes:
      - SCRAM
  agent:
    logLevel: DEBUG
  statefulSet:
    spec:
      template:
        spec:
          containers:
          - name: mongodb-enterprise-database
            resources:
              limits:
                cpu: "2"
                memory: 2Gi
              requests:
                cpu: "1"
                memory: 1Gi
EOF
```

### 5. Wait for MongoDB Enterprise Resource to be Ready

After applying the `MongoDB` custom resource, the operator begins deploying the MongoDB nodes (pods). This step uses `kubectl wait` to pause execution until the `mdb-rs` resource's status phase becomes `Running`, indicating that the MongoDB Enterprise replica set is operational.

[code_snippets/0310_wait_for_database_resource.sh](code_snippets/0310_wait_for_database_resource.sh)
```shell copy
echo "Waiting for MongoDB resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdb/mdb-rs --timeout=400s
echo; echo "MongoDB resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdb/mdb-rs
echo; echo "Pods running in cluster ${K8S_CLUSTER_0_CONTEXT_NAME}"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get pods
```

### 6. Create MongoDB Users

MongoDB requires authentication for secure access. This step creates three Kubernetes secrets: `mdb-admin-user-password`, `mdb-rs-search-sync-source-password`, and `mdb-user-password`. These secrets store the credentials for the MongoDB administrative user, the MongoDB Search service, and a dedicated user, respectively. These secrets will be mounted into the MongoDB pods.

These secrets are then used to create the following users:
* `mdb-admin` - root user that restores the `sample_mflix` database from backup.
* `search-sync-source` - user that the MongoDB Search service is authenticating to the MongoDB Server as in order to manage and build indexes.
* `mdb-user` - a regular user that will execute search queries.


[code_snippets/0315_create_mongodb_users.sh](code_snippets/0315_create_mongodb_users.sh)
```shell copy
# admin user with root role
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdb-admin-user-password \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}"
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: mdb-admin
spec:
  username: mdb-admin
  db: admin
  mongodbResourceRef:
    name: mdb-rs
  passwordSecretKeyRef:
    name: mdb-admin-user-password
    key: password
  roles:
  - name: root
    db: admin
EOF

# user used by MongoDB Search to connect to MongoDB database to synchronize data from
# For MongoDB <8.2, the operator will be creating the searchCoordinator custom role automatically
# From MongoDB 8.2, searchCoordinator role will be a built-in role.
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdb-rs-search-sync-source-password \
  --from-literal=password="${MDB_SEARCH_SYNC_USER_PASSWORD}"
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: search-sync-source-user
spec:
  username: search-sync-source
  db: admin
  mongodbResourceRef:
    name: mdb-rs
  passwordSecretKeyRef:
    name: mdb-rs-search-sync-source-password
    key: password
  roles:
  - name: searchCoordinator
    db: admin
EOF

# user performing search queries
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic mdb-user-password \
  --from-literal=password="${MDB_USER_PASSWORD}"
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBUser
metadata:
  name: mdb-user
spec:
  username: mdb-user
  db: admin
  mongodbResourceRef:
    name: mdb-rs
  passwordSecretKeyRef:
    name: mdb-user-password
    key: password
  roles:
  - name: readWrite
    db: sample_mflix
EOF

```
Ensure these secrets and users are created in the same namespace where you deploy MongoDB Server and Search.

### 7. Create MongoDB Search Resource

Once your MongoDB deployment is ready, enable Search capabilities by creating a `MongoDBSearch` custom resource, also named `mdb-rs` to associate it with the MongoDB instance. This resource specifies the CPU and memory resource requirements for the search nodes.

Note: Public Preview of MongoDB Search comes with some limitations, and it is not suitable for production use:
* Only one instance of the search node is supported (load balancing is not supported)

[code_snippets/0320_create_mongodb_search_resource.sh](code_snippets/0320_create_mongodb_search_resource.sh)
```shell copy
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: mdb-rs
spec:
  resourceRequirements:
    limits:
      cpu: "3"
      memory: 5Gi
    requests:
      cpu: "2"
      memory: 3Gi
EOF
```

### 8. Wait for Search Resource to be Ready

Similar to the MongoDB deployment, the Search deployment needs time to initialize. This step uses `kubectl wait` to pause until the `MongoDBSearch` resource `mdb-rs` reports a `Running` status in its `.status.phase` field, indicating that the search nodes are operational and integrated.

[code_snippets/0325_wait_for_search_resource.sh](code_snippets/0325_wait_for_search_resource.sh)
```shell copy
echo "Waiting for MongoDBSearch resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdbs/mdb-rs --timeout=300s
```
This command polls the status of the `MongoDBSearch` resource `mdb-rs`.

### 9. Verify MongoDB Enterprise Resource Status

Double-check the status of your `MongoDB` resource to ensure it remains healthy and that the integration with the Search resource is reflected if applicable.

[code_snippets/0330_wait_for_database_resource.sh](code_snippets/0330_wait_for_database_resource.sh)
```shell copy
echo "Waiting for MongoDB resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdb/mdb-rs --timeout=400s
```
This provides a final confirmation that the core database is operational.

### 10. List Running Pods

View all the running pods in your namespace. You should see pods for the MongoDB replica set members, the MongoDB Kubernetes Operator, and the MongoDB Search nodes.

[code_snippets/0335_show_running_pods.sh](code_snippets/0335_show_running_pods.sh)
```shell copy
echo; echo "MongoDB resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdb/mdb-rs
echo; echo "MongoDBSearch resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdbs/mdb-rs
echo; echo "Pods running in cluster ${K8S_CLUSTER_0_CONTEXT_NAME}"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get pods
```

## Using MongoDB Search

Now that your MongoDB Enterprise database with Search is deployed, you can start using its search capabilities.

### 11. Deploy MongoDB Tools Pod

To interact with your MongoDB deployment, this step deploys a utility pod named `mongodb-tools-pod`. This pod runs a MongoDB Enterprise Server image and is kept running with a `sleep infinity` command, allowing you to use `kubectl exec` to run MongoDB client tools like `mongosh` and `mongorestore` from within the Kubernetes cluster. Running steps in a pod inside the cluster simplifies connectivity to your MongoDB deployment without neeeding to expose the database externally (provided steps directly connect to the *.cluster.local hostnames).

[code_snippets/0410_run_mongodb_tools_pod.sh](code_snippets/0410_run_mongodb_tools_pod.sh)
```shell copy
#!/bin/bash

kubectl apply -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: mongodb-tools-pod
  labels:
    app: mongodb-tools
spec:
  containers:
  - name: mongodb-tools
    image: quay.io/mongodb/mongodb-enterprise-server:${MDB_VERSION}-ubi8
    command: ["/bin/bash", "-c"]
    args: ["sleep infinity"]
  restartPolicy: Never
EOF

echo "Waiting for the mongodb-tools pod to be ready..."
kubectl wait --for=condition=Ready pod/mongodb-tools-pod -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
```

### 12. Import Sample Data

To test the search functionality, this step imports the `sample_mflix.movies` collection. It downloads the sample dataset and uses `mongorestore` to load the data into the `sample_mflix` database in your MongoDB deployment, connecting as the admin user.

[code_snippets/0420_import_movies_mflix_database.sh](code_snippets/0420_import_movies_mflix_database.sh)
```shell copy
#!/bin/bash

kubectl exec -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo "Downloading sample database archive..."
curl https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive
echo "Restoring sample database"
mongorestore --archive=/tmp/sample_mflix.archive --verbose=1 --drop --nsInclude 'sample_mflix.*' \
--uri="mongodb://mdb-admin:${MDB_ADMIN_USER_PASSWORD}@mdb-rs-0.mdb-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdb-rs"
EOF
)"
```
This command uses `mongorestore` from the `mongodb-tools-pod` to load data from the downloaded `sample_mflix.archive` file.

### 13. Create Search Index

Before performing search queries, create a search index. This step uses `kubectl exec` to run `mongosh` in the `mongodb-tools-pod`. It connects to the `sample_mflix` database as `search-user` and calls `db.movies.createSearchIndex()` to create a search index named "default" with dynamic mappings on the `movies` collection. Dynamic mapping automatically indexes all fields with supported types. MongoDB Search offers flexible index definitions, allowing for dynamic and static field mappings, various analyzer types (standard, language-specific, custom), and features like synonyms and faceted search.

[code_snippets/0430_create_search_index.sh](code_snippets/0430_create_search_index.sh)
```shell copy
#!/bin/bash

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
  mongosh --quiet "mongodb://mdb-user:${MDB_USER_PASSWORD}@mdb-rs-0.mdb-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdb-rs" \
    --eval "use sample_mflix" \
    --eval 'db.movies.createSearchIndex("default", { mappings: { dynamic: true } });'
```

### 14. Wait for Search Index to be Ready

Creating a search index is an asynchronous operation. This script polls periodically the status by executing `db.movies.getSearchIndexes("default")`.

[code_snippets/0440_wait_for_search_index_ready.sh](code_snippets/0440_wait_for_search_index_ready.sh)
```shell copy
#!/bin/bash

# Currently it's not possible to check the status of search indexes, we need to just wait
echo "Sleeping to wait for search indexes to be created"
sleep 60
```

### 15. Execute a Search Query

Once the search index is ready, execute search queries using the `$search` aggregation pipeline stage. MongoDB Search supports a query language, allowing for various types of queries such as text search, autocomplete, faceting, and more. You can combine `$search` with other aggregation stages to further refine and process your results.

[code_snippets/0450_execute_search_query.sh](code_snippets/0450_execute_search_query.sh)
```shell copy
#!/bin/bash

mdb_script=$(cat <<'EOF'
use sample_mflix;
db.movies.aggregate([
  {
    $search: {
      "compound": {
        "must": [ {
          "text": {
            "query": "baseball",
            "path": "plot"
          }
        }],
        "mustNot": [ {
          "text": {
            "query": ["Comedy", "Romance"],
            "path": "genres"
          }
        } ]
      },
      "sort": {
        "released": -1
      }
    }
  },
  {
    $limit: 3
  },
  {
    $project: {
      "_id": 0,
      "title": 1,
      "plot": 1,
      "genres": 1,
      "released": 1
    }
  }
]);
EOF
)

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo '${mdb_script}' > /tmp/mdb_script.js
mongosh --quiet "mongodb://mdb-user:${MDB_USER_PASSWORD}@mdb-rs-0.mdb-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdb-rs" < /tmp/mdb_script.js
EOF
)"
```