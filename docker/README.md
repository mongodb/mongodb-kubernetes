# MongoDB Enterprise Kubernetes

This directory hosts the components that make up the MongoDB for Kubernetes solution.

The components are:
- [mongodb-enterprise-database](mongodb-enterprise-database): a container which comes with an automation agent preinstalled, allowing easy installation of MongoDB
- [mongodb-enterprise-operator](mongodb-enterprise-operator): the Kubernetes operator which enables provisioning of databases via Kubernetes CLI commands (e.g., `kubectl`)
- [mongodb-enteprise-ops-manager](mongodb-enteprise-ops-manager): a MongoDB Ops Manager 4.0 container
- [node-mongo-app](node-mongo-app): a simple app used to test connectivity to a provided database

## RHEL Based Images for OpenShift ##

To build the images based in RHEL go [here](https://github.com/10gen/kubernetes-rhel-images).

# Installation

While the images provided above should run in most Kubernetes flavours, we support automated installation in the following environments (using the [mdbk.sh](./mdbk.sh) script).

- Minikube
- Openshift (CLI)

These are intended to be used for testing and not production environments.  Additional steps need to be taken to ensure the proper security measures are put in-place.


## Minikube

### Prerequisites

- [Install Minikube and kubectl](https://kubernetes.io/docs/tasks/tools/install-minikube/)

### Create and configure a cluster

The following command will create a Minikube cluster, deploy the Operator and Ops Manager containers, and pre-configure Ops Manager.

``` bash
./mdbk.sh --minikube --ops-manager
```

**Note:** The Ops Manager container takes several seconds to initialize; logs (see below) may not be available right away.

After it has started, its URL will be printed and a new browser window pointing to it will open (MacOS only).

### Delete a cluster

``` bash
./mdbk.sh --minikube --undeploy
```

### Other useful commands

* **Follow Ops Manager's logs:** `kubectl -n mongodb logs -f mongodb-enterprise-ops-manager-0`
* **Follow the Operator's logs:** `kubectl -n mongodb logs -f deployment/mongodb-enterprise-operator`
* **Connect to the Ops Manager container**: `kubectl -n mongodb exec -it mongodb-enterprise-ops-manager-0 /bin/bash`
* **See pod status:** `kubectl -n mongodb get pods`


## Openshift

This section contains instructions for configuring the following:
- a local [Openshift Cluster](https://docs.openshift.org/latest/getting_started/administrators.html)
- [Helm and Tiller](https://docs.helm.sh/install/)
- [MongoDB Enterprise Kubernetes Operator](../public)
- [MongoDB Ops Manager](mongodb-enterprise-ops-manager/)


### Prerequisites

- [Homebrew](https://brew.sh/) (MacOS only)
- [Docker](https://docs.docker.com/install/)
- Configure xhyve (MacOS only)
  ```bash
  brew install docker docker-compose docker-machine xhyve docker-machine-driver-xhyve
  sudo chown root:wheel $(brew --prefix)/opt/docker-machine-driver-xhyve/bin/docker-machine-driver-xhyve
  sudo chmod u+s $(brew --prefix)/opt/docker-machine-driver-xhyve/bin/docker-machine-driver-xhyve
  docker-machine create default --driver xhyve --xhyve-experimental-nfs-share
  docker-machine env default
  ```
- [Openshift-CLI 3.9.0](https://docs.openshift.com/container-platform/3.9/cli_reference/get_started_cli.html)
  ```bash
  pushd /tmp
  if uname -a | grep -q Darwin; then
    curl -Lo openshift-origin-client.zip https://github.com/openshift/origin/releases/download/v3.9.0/openshift-origin-client-tools-v3.9.0-191fece-mac.zip
    unzip openshift-origin-client.zip
    rm openshift-origin-client.zip
    sudo mv oc /usr/local/bin/oc
  elif uname -a | grep -q Linux; then
    curl -Lo openshift-origin-client.tar.gz https://github.com/openshift/origin/releases/download/v3.9.0/openshift-origin-client-tools-v3.9.0-191fece-linux-64bit.tar.gz
    tar xzf openshift-origin-client.tar.gz
    sudo mv openshift-origin-client-tools-v3.9.0-191fece-linux-64bit/oc /usr/local/bin/oc
  fi
  popd
  ```
- [Getting started with Helm in Openshift](https://blog.openshift.com/getting-started-helm-openshift/)
  ```bash
  # Alternatively (MacOS), install a specific version (2.9.0) and add it to your path
  cd "${HOME}"
  curl -s https://storage.googleapis.com/kubernetes-helm/helm-v2.9.0-darwin-amd64.tar.gz | tar xz
  darwin-amd64/helm init --client-only
  export PATH=$(pwd)/darwin-amd64:${PATH}
  ```

**NOTES:**
- Read more about `docker-machine-driver-xhyve` of the [Minishift virtualization env](https://docs.openshift.org/latest/minishift/getting-started/setting-up-virtualization-environment.html).
- You will need to allocate at least 4GB of RAM to the Docker process, otherwise the Ops Manager container will be unable to start!


### Create and configure a cluster

The following command will create an Openshift cluster, deploy the Operator and Ops Manager containers, and pre-configure Ops Manager.

``` bash
./mdbk.sh --openshift --ops-manager
```

**Note:** The Ops Manager container takes several seconds to initialize; logs (see below) may not be available right away.

After it has started, its URL will be printed and a new browser window pointing to it will open (MacOS only).

### Delete a cluster

``` bash
./mdbk.sh --openshift --undeploy
```

### Other useful commands

* **Select the default project:** `oc project mongodb`
* **Follow Ops Manager's logs:** `oc logs -f mongodb-enterprise-ops-manager-0`
* **Follow the Operator's logs:** `oc logs -f deployment/mongodb-enterprise-operator`
* **Connect to the Ops Manager container**: `oc exec -it mongodb-enterprise-ops-manager-0 /bin/bash`
* **See pod status:** `oc get pods`


## AWS ECS

First, configure your `kubectl` context and ensure you have an available Kubernetes cluster.

If you haven't previously set one up, perform all the steps in the next section.

Finally, run the following command to deploy the operator and Ops Manager containers:

```bash
./mdbk.sh --operator
```

Alternatively, the Helm deployment can be customized to fit any of your needs, for example:

```bash
# Operator
helm install  --tiller-namespace "tiller" --namespace "mongodb" --name mongodb-enterprise \
    --set registry.repository="quay.io/mongodb-enterprise-private" \  # Custom Docker Registry
    --set registry.pullPolicy="Never" \                               # Don't pull images
    --set operator.version="0.2" \                                    # Custom Operator Version
    "../public/helm_chart" -f "mongodb-enterprise-ops-manager/helm_chart/values.yaml"

# Ops Manager
helm install  --tiller-namespace "tiller" --namespace "mongodb" --name mongodb-enterprise \
    --set registry.repository="quay.io/mongodb-enterprise-private" \  # Custom Docker Registry
    --set registry.pullPolicy="Never" \                               # Don't pull images
    --set opsManager.version="4.1.0" \                                # Custom Ops Manager Version
    "mongodb-enterprise-ops-manager/helm_chart" -f "mongodb-enterprise-ops-manager/helm_chart/values.yaml"
```


### Configuring a Kubernetes cluster in AWS ECS (kops)

* 1\. Define the following environment variables

```bash
export KOPS_STATE_STORE="s3://kube-om-state-store"
export CLUSTER_NAME="your-cluster-name"
export CLUSTER="${CLUSTER_NAME}.mongokubernetes.com"
export AWS_IMAGE_REPO="268558157000.dkr.ecr.us-east-1.amazonaws.com"
```

* 2\. Create a cluster definition file

```bash
# Ensure you are logged in
eval "$(shell aws ecr get-login --no-include-email --region us-east-1)"

kops create cluster "${CLUSTER}" \
    --node-count 3 \
    --zones us-east-1a,us-east-1b,us-east-1c \
    --node-size t2.xlarge \
    --master-size=t2.xlarge \
    --kubernetes-version=v1.10.3 \
    --ssh-public-key=~/.ssh/id_rsa.pub \
    --authorization RBAC \
    --dry-run \
    -o yaml > "${HOME}/${CLUSTER}.yaml"
```

* 3\. Add the following to the generated file (under the `spec:` section)

Edit the file: `vi "${HOME}/${CLUSTER}.yaml"` and add the following:

```yaml
  kubeAPIServer:
    authorizationRbacSuperUser: admin
```

* 4\. Create the cluster from the definition file, then deploy it

```bash
kops create -f "${HOME}/${CLUSTER}.yaml"
kops create secret --name "${CLUSTER}" sshpublickey admin -i ~/.ssh/id_rsa.pub
kops update cluster "${CLUSTER}" --yes
```

* 5\. Build and push the artifacts to the ECS repository

The following script uses the `CLUSTER_NAME` and `AWS_IMAGE_REPO` environment variables to determine where to push the images.
The advantage of using that is that ECS clusters can pull images without requiring any further authentication.

```bash
./docker-build-and-push.sh
```

* 6\. Configure Helm

```bash
./mdbk.sh --helm
```


### Deleting an existing AWS ECS cluster

You can delete any clusters with: `kops delete cluster $CLUSTER --yes`

Ensure you are not deleting someone else's cluster!
