# Ops Manager

## Manual set-up steps

**NOTE: Make sure you use the Ops Manager of version >= 4.0!**

After Ops Manager is started (possibly in mci) you need to perform some actions to get it ready for communication with
Kubernetes Mongodb Operator:

1. Generate public api key (click user name in top right corner and select "Public API Access" tab). Write down the 
key for later usage
2. Add Kubernetes cluster hosts to "API Whitelist" section. If you use `Minikube` then search for *myip* in google and
copy the ip address shown. If you have your cluster deployed in AWS or GCP then you need to add external ips of all 
non-master nodes
3. Write down the group id (you can take it directly from url) and host of Ops Manager - this will be necessary for 
configuring Operator later


## Manual Ops Manager Kubernetes configuration

If you chose to skip [automatic configuration in Ops Manager](../docker/mongodb-enterprise-ops-manager-dev/#auto-configuration) by setting `SKIP_OPS_MANAGER_REGISTRATION` environment variable,
you will need to perform the following steps to correctly configure the [mongodb-enterprise-operator](../docker/mongodb-enterprise-operator).

- 1\. Register an admin user
  ```bash
  open "${OM_HOST}/user#/ops/register/accountProfile"
  ```

- 2\. Configure the project's id (group id) and the user's name
  ```bash
  export OM_USER="..."
  export OM_PROJECT_ID="..."
  ```

- 3\. Generate a public API key (configure OM_API_KEY)
  ```bash
  # OM -> Username -> Account -> Public API Access
  open ${OM_HOST}/v2#/account/publicApi  
  export OM_API_KEY="..."
  ```

- 4\. On the same page, add `0.0.0.0/0` (or a more restrictive IP range) as an *API whitelist*

- 5\. Create a ConfigMap for the project

```bash
cat <<EOF | kubectl --namespace "mongodb-resources" apply -f -
---
apiVersion: v1
kind: ConfigMap
metadata:
    name: my-project
    namespace: mongodb-resources
data:
    projectId: ${OM_PROJECT_ID}
    baseUrl: ${OM_HOST}
EOF
```

- 6\. Create a secret for holding the Ops Manager API credentials

```bash
kubectl --namespace mongodb-resources create secret generic my-credentials --from-literal=user="${OM_USER}" --from-literal=publicApiKey="${OM_API_KEY}"
```
