# MongoDB Community Search on Kubernetes - Quick Start

This guide provides instructions for deploying MongoDB Community Edition along with its Search capabilities onto a Kubernetes cluster. By following these steps, you will set up a MongoDB instance and configure search indexes to perform full-text search queries against your data.

## Prerequisites

Before you begin, ensure you have the following tools and configurations in place:

- **Kubernetes cluster**: A running Kubernetes cluster (e.g., Minikube, Kind, GKE, EKS, AKS).
- **kubectl**: The Kubernetes command-line tool, configured to communicate with your cluster.
- **Helm**: The package manager for Kubernetes, used here to install the MongoDB Kubernetes Operator.
- **Bash 5.1+**: All shell commands in this guide are intended to be run in Bash. Scripts in this guide are automatically tested on Linux with Bash 5.1.

## Setup Steps

The following steps guide you through deploying MongoDB Community with Search. Each step provides a shell script.
**It is important to first source the `env_variables.sh` script provided and customize its values for your environment.**
The subsequent script snippets rely on the environment variables defined in `env_variables.sh`. You should copy and paste each script into your Bash terminal.

### 1. Configure Environment Variables

First, you need to set up your environment. The `env_variables.sh` script, shown below, contains variables for the subsequent steps. You should create this file locally or use the linked one.

Download or copy the content of `env_variables.sh`:
[env_variables.sh](env_variables.sh)
```shell copy
# set it to the context name of the k8s cluster
export K8S_CLUSTER_0_CONTEXT_NAME="<local cluster context>"

# At the private preview stage the community search image is accessible only from a private repository.
# Please contact MongoDB Support to get access.
export PRIVATE_PREVIEW_IMAGE_PULLSECRET="<.dockerconfigjson>"

# the following namespace will be created if not exists
export MDB_NAMESPACE="mongodb"

export MDB_ADMIN_USER_PASSWORD="admin-user-password-CHANGE-ME"
export MDB_SEARCH_SYNC_USER_PASSWORD="search-user-password-CHANGE-ME"

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

Next, install the MongoDB Kubernetes Operator from the Helm repository you just added. The Operator will watch for MongoDBCommunity and MongoDBSearch custom resources and manage the lifecycle of your MongoDB deployments.

[code_snippets/0100_install_operator.sh](code_snippets/0100_install_operator.sh)
```shell copy
helm upgrade --install --debug --kube-context "${K8S_CLUSTER_0_CONTEXT_NAME}" \
  --create-namespace \
  --namespace="${MDB_NAMESPACE}" \
  mongodb-kubernetes \
  --set "${OPERATOR_ADDITIONAL_HELM_VALUES:-"dummy=value"}" \
  "${OPERATOR_HELM_CHART}"
```
This command installs the operator in the `mongodb` namespace (creating it if it doesn't exist) and names the release `community-operator`.

### 4. Configure Pull Secret for MongoDB Community Search

To use MongoDB Search, your Kubernetes cluster needs to pull the necessary container images. This step creates a Kubernetes secret named `community-private-preview-pullsecret`. This secret stores the credentials required to access the image repository for MongoDB Search. The script then patches the `mongodb-kubernetes-database-pods` service account to include this pull secret, allowing pods managed by this service account to pull the required images.

[code_snippets/0200_configure_community_search_pullsecret.sh](code_snippets/0200_configure_community_search_pullsecret.sh)
```shell copy
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: community-private-preview-pullsecret
data:
  .dockerconfigjson: "${PRIVATE_PREVIEW_IMAGE_PULLSECRET}"
type: kubernetes.io/dockerconfigjson
EOF

pull_secrets=$(kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
  get sa mongodb-kubernetes-database-pods -n "${MDB_NAMESPACE}" -o=jsonpath='{.imagePullSecrets[*]}')

if [[ "${pull_secrets}" ]]; then
  kubectl patch --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
    sa mongodb-kubernetes-database-pods  \
    --type=json -p='[{"op": "add", "path": "/imagePullSecrets/-", "value": {"name": "community-private-preview-pullsecret"}}]'
else
  kubectl patch --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" \
    sa mongodb-kubernetes-database-pods  \
    --type=merge -p='{"imagePullSecrets": [{"name": "community-private-preview-pullsecret"}]}'
fi
echo "ServiceAccount mongodb-kubernetes-database-pods has been patched: "

kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o yaml sa mongodb-kubernetes-database-pods
```
This script creates a `community-private-preview-pullsecret` secret in your Kubernetes namespace and associates it with the service account used for MongoDB pods.

### 5. Verify Pull Secret Configuration

Confirm that the `community-private-preview-pullsecret` has been successfully added to the `mongodb-kubernetes-database-pods` service account. This ensures that Kubernetes can authenticate with the container registry when pulling images for MongoDB Search pods.

[code_snippets/0210_verify_community_search_pullsecret.sh](code_snippets/0210_verify_community_search_pullsecret.sh)
```shell copy
echo "Verifying mongodb-kubernetes-database-pods contains proper pull secret"
if ! kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o json \
  sa mongodb-kubernetes-database-pods -o=jsonpath='{.imagePullSecrets[*]}' | \
    grep community-private-preview-pullsecret; then
  echo "ERROR: mongodb-kubernetes-database-pods service account doesn't contain necessary pullsecret"
  kubectl get --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -o json \
    sa mongodb-kubernetes-database-pods -o=yaml
  return 1
fi
```
This command checks the `mongodb-kubernetes-database-pods` service account to confirm the presence of `community-private-preview-pullsecret`.

## Creating a MongoDB Community Search Deployment

With the prerequisites and initial setup complete, you can now deploy MongoDB Community Edition and enable Search.

### 6. Create MongoDB User Secrets

MongoDB requires authentication for secure access. This step creates two Kubernetes secrets: `admin-user-password` and `search-user-password`. These secrets store the credentials for the MongoDB administrative user and a dedicated search user, respectively. These secrets will be mounted into the MongoDB pods.

[code_snippets/0305_create_mongodb_community_user_secrets.sh](code_snippets/0305_create_mongodb_community_user_secrets.sh)
```shell copy
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic admin-user-password \
  --from-literal=password="${MDB_ADMIN_USER_PASSWORD}"

kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --namespace "${MDB_NAMESPACE}" \
  create secret generic search-user-password \
  --from-literal=password="${MDB_SEARCH_SYNC_USER_PASSWORD}"
```
Ensure these secrets are created in the same namespace where you plan to deploy MongoDB.

### 7. Create MongoDB Community Resource

Now, deploy MongoDB Community by creating a `MongoDBCommunity` custom resource named `mdbc-rs`. This resource definition instructs the MongoDB Kubernetes Operator to configure a MongoDB replica set with 3 members, running version 8.0.6. MongoDB Community Search is supported only from MongoDB Community Server version 8.0. It also defines CPU and memory resources for the `mongod` and `mongodb-agent` containers, and sets up two users (`admin-user` and `search-user`) with their respective roles and password secrets. User `search-user` will be used to restore, connect and perform search queries on the `sample_mflix` database.

[code_snippets/0310_create_mongodb_community_resource.sh](code_snippets/0310_create_mongodb_community_resource.sh)
```yaml copy
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: mdbc-rs
spec:
  version: 8.0.6
  type: ReplicaSet
  members: 3
  security:
    authentication:
      ignoreUnknownUsers: true
      modes:
        - SCRAM
  agent:
    logLevel: INFO
  statefulSet:
    spec:
      template:
        spec:
          containers:
            - name: mongod
              resources:
                limits:
                  cpu: "3"
                  memory: 5Gi
                requests:
                  cpu: "2"
                  memory: 5Gi
            - name: mongodb-agent
              resources:
                limits:
                  cpu: "2"
                  memory: 5Gi
                requests:
                  cpu: "1"
                  memory: 5Gi
  users:
    - name: admin-user
      passwordSecretRef:
        name: admin-user-password
      roles:
        - db: admin
          name: clusterAdmin
        - db: admin
          name: userAdminAnyDatabase
      scramCredentialsSecretName: admin-user
    - name: search-user
      passwordSecretRef:
        name: search-user-password
      roles:
        - db: sample_mflix
          name: dbOwner
      scramCredentialsSecretName: search-user
EOF
```

### 8. Wait for MongoDB Community Resource to be Ready

After applying the `MongoDBCommunity` custom resource, the operator begins deploying the MongoDB nodes (pods). This step uses `kubectl wait` to pause execution until the `mdbc-rs` resource's status phase becomes `Running`, indicating that the MongoDB Community replica set is operational.

[code_snippets/0315_wait_for_community_resource.sh](code_snippets/0315_wait_for_community_resource.sh)
```shell copy
echo "Waiting for MongoDBCommunity resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdbc/mdbc-rs --timeout=400s
echo; echo "MongoDBCommunity resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdbc/mdbc-rs
echo; echo "Pods running in cluster ${K8S_CLUSTER_0_CONTEXT_NAME}"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get pods
```

### 9. Create MongoDB Search Resource

Once your MongoDB deployment is ready, enable Search capabilities by creating a `MongoDBSearch` custom resource, also named `mdbc-rs` to associate it with the MongoDB instance. This resource specifies the CPU and memory resource requirements for the search nodes.

Note: Private preview of MongoDB Community Search comes with some limitations, and it is not suitable for production use:
* TLS cannot be enabled in MongoDB Community deployment (MongoD communicates with MongoT with plain text).
* Only one node of search node is supported (load balancing not supported)

[code_snippets/0320_create_mongodb_search_resource.sh](code_snippets/0320_create_mongodb_search_resource.sh)
```shell copy
kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: mdbc-rs
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

The `MongoDBSearch.spec` fields are supported:
* `spec.source.mongodbResourceRef.name` - omitted in the example as the MongoDBSearch CR has the same name as MongoDBCommunity CR allowing to integrate both using naming convention. While keeping the same name is recommended (you cannot have more than one MongoDBSearch resources referencing the same MongoDBCommunity resource - it's 1:1 relationship) it's not enforced. The name can be different, but then you must explicitly point to the MongoDBCommunity you would like to enable search in. Note that you enable search capabilities by deploying search component (with MongoDBSearch CR) and nothing is necessary to define in MongoDBCommunity CR to configure it for search - it will be configured automatically by recognising there is related MongoDBSearch pointing to it.
* `spec.version`: Version of mongodb-community-search. By default, the operator chooses the MongoDB Search version automatically, but it is possible to specify it explicitly. Currently, the default value is `1.47.0`.
* `spec.statefulSet`: Optional statefulset overrides, which are applied last to the mongot's statefulset. It is possible to adjust any statefulset configuration that was create by the operator (the overrides are applied last). The type of the field is [apps/v1/StatefulSet](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.26/#statefulset-v1-apps) and both `spec.statefulSet.spec` and `spec.statefulSet.metadata` fields are supported.
* `spec.persistence.single`: optional storage configuration for MongoDB Search persistence volume containing storing search indexes. See [here](https://www.mongodb.com/docs/kubernetes/current/reference/k8s-operator-specification/#mongodb-setting-spec.podSpec.persistence.single) for more information about storage settings. MongoDBSearch reuses the same persistence type as in other custom resources (e.g. `MongoDB`), but supports only `single` persistence field. If not set, the operator sets `spec.persistence.single.storage = 10G`.
* `spec.resourceRequirements` - resource requests and limits for mongodb-search container. It's recommended to use this field to customize resource allocations instead of overriding it via `spec.statefulSet` overrides. If not set, the operator sets the following values (no limits, only requests):
```yaml
requests:
    cpu: 2
    memory: 2G
```

### 10. Wait for Search Resource to be Ready

Similar to the MongoDB deployment, the Search deployment needs time to initialize. This step uses `kubectl wait` to pause until the `MongoDBSearch` resource `mdbc-rs` reports a `Running` status in its `.status.phase` field, indicating that the search nodes are operational and integrated.

[code_snippets/0325_wait_for_search_resource.sh](code_snippets/0325_wait_for_search_resource.sh)
```shell copy
echo "Waiting for MongoDBSearch resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdbs/mdbc-rs --timeout=300s
```
This command polls the status of the `MongoDBSearch` resource `mdbc-rs`.

### 11. Verify MongoDB Community Resource Status

Double-check the status of your `MongoDBCommunity` resource to ensure it remains healthy and that the integration with the Search resource is reflected if applicable.

[code_snippets/0330_wait_for_community_resource.sh](code_snippets/0330_wait_for_community_resource.sh)
```shell copy
echo "Waiting for MongoDBCommunity resource to reach Running phase..."
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" wait --for=jsonpath='{.status.phase}'=Running mdbc/mdbc-rs --timeout=400s
```
This provides a final confirmation that the core database is operational.

### 12. List Running Pods

View all the running pods in your namespace. You should see pods for the MongoDB replica set members, the MongoDB Kubernetes Operator, and the MongoDB Search nodes.

[code_snippets/0335_show_running_pods.sh](code_snippets/0335_show_running_pods.sh)
```shell copy
echo; echo "MongoDBCommunity resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdbc/mdbc-rs
echo; echo "MongoDBSearch resource"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get mdbs/mdbc-rs
echo; echo "Pods running in cluster ${K8S_CLUSTER_0_CONTEXT_NAME}"
kubectl --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" get pods
```

## Using MongoDB Search

Now that your MongoDB Community database with Search is deployed, you can start using its search capabilities.

### 13. Deploy MongoDB Tools Pod

To interact with your MongoDB deployment, this step deploys a utility pod named `mongodb-tools-pod`. This pod runs a MongoDB Community Server image and is kept running with a `sleep infinity` command, allowing you to use `kubectl exec` to run MongoDB client tools like `mongosh` and `mongorestore` from within the Kubernetes cluster. Running steps in a pod inside the cluster simplifies connectivity to mongodb without neeeding to expose the database externally (provided steps directly connect to the *.cluster.local hostnames).

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
    image: mongodb/mongodb-community-server:8.0.6-ubi9
    command: ["/bin/bash", "-c"]
    args: ["sleep infinity"]
  restartPolicy: Never
EOF

echo "Waiting for the mongodb-tools to be ready..."
kubectl wait --for=condition=Ready pod/mongodb-tools-pod -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" --timeout=60s
```

### 14. Import Sample Data

To test the search functionality, this step imports the `sample_mflix.movies` collection. It downloads the sample dataset and uses `mongorestore` to load the data into the `sample_mflix` database in your MongoDB deployment, connecting as the `search-user`.

[code_snippets/0420_import_movies_mflix_database.sh](code_snippets/0420_import_movies_mflix_database.sh)
```shell copy
#!/bin/bash

kubectl exec -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo "Downloading sample database archive..."
curl https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive
echo "Restoring sample database"
mongorestore --archive=/tmp/sample_mflix.archive --verbose=1 --drop --nsInclude 'sample_mflix.*' --uri="mongodb://search-user:${MDB_SEARCH_SYNC_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs"
EOF
)"
```
This command uses `mongorestore` from the `mongodb-tools-pod` to load data from the downloaded `sample_mflix.archive` file.

### 15. Create Search Index

Before performing search queries, create a search index. This step uses `kubectl exec` to run `mongosh` in the `mongodb-tools-pod`. It connects to the `sample_mflix` database as `search-user` and calls `db.movies.createSearchIndex()` to create a search index named "default" with dynamic mappings on the `movies` collection. Dynamic mapping automatically indexes all fields with supported types. MongoDB Search offers flexible index definitions, allowing for dynamic and static field mappings, various analyzer types (standard, language-specific, custom), and features like synonyms and faceted search.

[code_snippets/0430_create_search_index.sh](code_snippets/0430_create_search_index.sh)
```shell copy
#!/bin/bash

kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
  mongosh --quiet "mongodb://search-user:${MDB_SEARCH_SYNC_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" \
    --eval "use sample_mflix" \
    --eval 'db.movies.createSearchIndex("default", { mappings: { dynamic: true } });'
```

### 16. Wait for Search Index to be Ready

Creating a search index is an asynchronous operation. This script polls periodically the status by executing `db.movies.getSearchIndexes("default")`.

[code_snippets/0440_wait_for_search_index_ready.sh](code_snippets/0440_wait_for_search_index_ready.sh)
```shell copy
#!/bin/bash

for _ in $(seq 0 10); do
  search_index_status=$(kubectl exec --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" mongodb-tools-pod -- \
      mongosh --quiet "mongodb://search-user:${MDB_SEARCH_SYNC_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" \
        --eval "use sample_mflix" \
        --eval 'db.movies.getSearchIndexes("default")[0]["status"]')

  if [[ "${search_index_status}" == "READY" ]]; then
    echo "Search index is ready."
    break
  fi
  echo "Search index is not ready yet: status=${search_index_status}"
  sleep 2
done

if [[ "${search_index_status}" != "READY" ]]; then
  echo "Error waiting for the search index to be ready"
  return 1
fi
```

### 17. Execute a Search Query

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
mongosh --quiet "mongodb://search-user:${MDB_SEARCH_SYNC_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs" < /tmp/mdb_script.js
EOF
)"
```
