
# Migration Guide: MongoDB Community Operator (MCO) to MongoDB Kubernetes Operator (MCK)

This guide walks you through the complete process of migrating from the MongoDB Community Operator (MCO) to the MongoDB Kubernetes Operator (MCK). It ensures CRDs are preserved, services remain uninterrupted, and reconciliation is correctly handed over.
This guide ensures the CRDs are retained using Helm's keep annotation and transitions smoothly to MCK.

---

## 📋 Prerequisites

- Kubernetes cluster access with admin permissions.
- Helm installed and configured.
- MCO must be upgraded to a version that includes CRD keep annotations (`"helm.sh/resource-policy": keep`).

---

## 🚀 Migration Steps

### 1. Upgrade to the Latest MCO Chart with `keep` Annotations

Ensure CRDs will not be deleted on uninstall:

```bash
helm repo add mongodb https://mongodb.github.io/helm-charts
helm repo update
helm upgrade --install mongodb-community-operator mongodb/community-operator
```

✅ Verify that CRDs have the keep annotation:

```bash
kubectl get crds | grep mongodb
kubectl get crd mongodbcommunity.mongodbcommunity.mongodb.com -o yaml | grep 'helm.sh/resource-policy'
```

You should see:
```yaml
helm.sh/resource-policy: keep
```

---

### 2. Update Environment Variables

Rename environment variables to match MCK expectations:

| MCO Variable                | MCK Variable                     |
|----------------------------|----------------------------------|
| `MONGODB_REPO_URL`         | `MDB_COMMUNITY_REPO_URL`        |
| `MDB_IMAGE_TYPE`           | `MDB_COMMUNITY_IMAGE_TYPE`      |
| `MONGODB_IMAGE`            | `MDB_COMMUNITY_IMAGE`           |
| `AGENT_IMAGE`              | `MDB_COMMUNITY_AGENT_IMAGE`     |

These variables must be updated in your Helm values file to have templating set them in the operator
as environment variables.

---

### 3. Update community specific Helm Settings

All of the above environment variables can be configured in the values.yaml file.
They are all namespaced under `community`.

---

### 4. Scale Down the MCO Operator Deployment

Prevent the MCO reconciler from interfering with the MCK operator which we will install in a bit:

```bash
kubectl scale deployment mongodb-community-operator --replicas=0
```

---

### 5. Install the MCK Chart

Deploy the new MCK Helm release with your updated values:

```bash
helm install mongodb-kubernetes-operator mongodb/enterprise-operator -f values.yaml
```

⚠️ Warning: Ensure the MCK chart is installed with a different release name than the prior community operator chart. By default, the new MCK chart uses a different `operator.name`, which differs from the community operator.
If you've modified the community operator's name/release name, ensure the MCK's `operator.name` value is different
to avoid RBAC conflicts, since service accounts, roles, and other resources are based on this name.

---

### 6. Let MCK Reconcile the Existing Resources

After installation:

- MCK will take control of existing MongoDB CRs.
- It will apply updated container images, RBAC settings, and other resources.
- A rolling restart will occur as service account names are updated among other things (e.g. to `mongodb-kubernetes-appdb`).

---

### 7. Wait for All Resources to Become Healthy

Monitor the cluster:

```bash
kubectl get pods
kubectl get mdbc -A
```

Wait until:

- All pods are running
- All MongoDB resources are reconciled
- No errors are shown in the MCK operator logs

---

### 8. Uninstall the MCO Chart

Once MCK has taken over, remove the MCO chart:

```bash
helm uninstall mongodb-community-operator
```

- Helm will retain the CRDs due to the `keep` annotation
- Old RBAC resources will be removed, but are no longer needed since we've installed new ones to use

---

## ✅ Final Verification

1. Check CRDs still exist:

```bash
kubectl get crds | grep mongodb
```

2. Ensure MCK logs show successful reconciliation.
3. Optionally clean up any leftover unused RBACs.

---

## 💬 Support

For questions or issues during the migration, refer to the [official MCK repository](https://github.com/mongodb/mongodb-kubernetes) or contact your support representative.
