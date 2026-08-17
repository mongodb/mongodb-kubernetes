# Multi-Cluster CLI GitOps Samples

This is an example of managing a multi-cluster MongoDB deployment in a [GitOps](https://en.wikipedia.org/wiki/DevOps#GitOps) operating model, using the `kubectl-mongodb` CLI as the manifest generator. For more details on managing multi-cluster resources with the Kubernetes operator see [the official documentation](https://www.mongodb.com/docs/kubernetes-operator/master/multi-cluster/). The example is applicable for an [ArgoCD](https://argo-cd.readthedocs.io/) configuration.

Multi-cluster topology is day-2 configuration: the operator is installed once on the central cluster, and each member cluster is onboarded with RBAC manifests, a credential Secret and a `MemberCluster` CR — all of which can live in Git.

## ArgoCD configuration
The files in the [argocd](./argocd) directory contain an [AppProject](./argocd/project.yaml) and an [Application](./argocd/application.yaml) linked to it which allows the synchronization of `MongoDBMultiCluster` resources such as [this replica set](./resources/replica-set.yaml) from a Git repo.

## GitOps flow

### 1. Install the operator on the central cluster
The central-cluster RBAC the operator needs for multi-cluster operation always ships with the operator installation, so nothing extra is required here.

### 2. Apply member-cluster RBAC per member cluster
Each member cluster needs RBAC that lets the operator manage workloads on it. The canonical way to produce it is the CLI:

``` shell
kubectl mongodb multicluster generate-member-resources \
  --member-cluster member-cluster --member-cluster-namespace mongodb | kubectl apply -f -
```

For users who cannot run the plugin, the [rbac](./resources/rbac) directory contains the checked-in output of that command:
- [namespace_scoped_member_cluster.yaml](./resources/rbac/namespace_scoped_member_cluster.yaml) — Role/RoleBinding for a member cluster watching a single namespace (the default).
- [cluster_scoped_member_cluster.yaml](./resources/rbac/cluster_scoped_member_cluster.yaml) — ClusterRole/ClusterRoleBinding for a member cluster when the operator is installed cluster-wide (`--operator-cluster-scoped`).

Adjust the names/namespaces for your clusters and apply (or commit) one file per member cluster.

### 3. Manage the credential Secret and MemberCluster CR per member cluster in Git
The operator learns about each member cluster from a credential Secret (a single-context kubeconfig authenticating as the member ServiceAccount created in step 2) and a `MemberCluster` CR referencing it, both in the operator's namespace on the central cluster. Both can be managed in Git:

``` yaml
apiVersion: v1
kind: Secret
metadata:
  name: mck-credential-member-cluster
  namespace: mongodb
type: Opaque
stringData:
  # Single-context kubeconfig with the member cluster's API server URL, CA and
  # ServiceAccount token. The real output comes from `generate-member-registration`.
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
      - name: member-cluster
        cluster:
          server: https://<member-cluster-api-server>
          certificate-authority-data: <base64-ca>
    users:
      - name: mck-operator
        user:
          token: <service-account-token>
    contexts:
      - name: member-cluster
        context:
          cluster: member-cluster
          user: mck-operator
          namespace: mongodb
    current-context: member-cluster
---
apiVersion: operator.mongodb.com/v1
kind: MemberCluster
metadata:
  name: member-cluster
  namespace: mongodb
spec:
  # Logical name referenced by clusterSpecList[].clusterName in workload CRs.
  clusterName: member-cluster
  credentialSecretRef:
    name: mck-credential-member-cluster
```

In practice, generate both documents from the live member cluster rather than hand-writing them:

``` shell
kubectl mongodb multicluster generate-member-registration \
  --member-cluster member-cluster --member-cluster-context member-cluster-ctx \
  --member-cluster-namespace mongodb --operator-namespace mongodb
```

Commit the output (or apply it) per member cluster. Note the Secret contains credentials — use your GitOps secret-management solution of choice for it.

### 4. Recovery
To recover a member cluster (or re-onboard after losing the central cluster), re-apply the `MemberCluster` CRs and credential Secrets from Git — the operator reconciles the member clusters from them.

## RBAC Settings for the Member clusters
The RBAC settings for the member clusters are typically created using the CLI. In cases where it is not possible, you can adjust and apply the YAML files from the [rbac](./resources/rbac) directory.

### Build the multi-cluster CLI image
You can build a minimal image containing the CLI executable using the `Dockerfile` [provided in this path](../../../cmd/kubectl-mongodb/Dockerfile).
``` shell
git clone https://github.com/mongodb/mongodb-kubernetes
cd mongodb-kubernetes/
docker build . -t "your-registry/multi-cluster-cli:latest" -f cmd/kubectl-mongodb/Dockerfile
docker push "your-registry/multi-cluster-cli:latest"
```
