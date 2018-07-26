# Deploying the operator with Openshift/Helm

This page contains instructions for configuring the following:
- a local [Openshift Cluster](https://docs.openshift.org/latest/getting_started/administrators.html)
- [Helm and Tiller](https://docs.helm.sh/install/)
- [MongoDB Enterprise Kubernetes Operator](../public)
- [MongoDB Ops Manager](../docker/mongodb-enterprise-ops-manager) running in Docker

**NOTE:** This guide assumes that you are running in a MacOS environment.

# Prerequisites

## Install Homebrew for MacOS

Full and up-to-date install instructions can be found on [Homebrew's homepage](https://brew.sh/)

```bash
/usr/bin/ruby -e "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/master/install)"
```

## Install Docker

```bash
brew install docker docker-compose docker-machine xhyve docker-machine-driver-xhyve
sudo chown root:wheel $(brew --prefix)/opt/docker-machine-driver-xhyve/bin/docker-machine-driver-xhyve
sudo chmod u+s $(brew --prefix)/opt/docker-machine-driver-xhyve/bin/docker-machine-driver-xhyve

# Create the first machine
docker-machine create default --driver xhyve --xhyve-experimental-nfs-share
docker-machine env default
```

For ease of use, download and run [Docker for Mac](https://download.docker.com/mac/stable/Docker.dmg).

Read more about `docker-machine-driver-xhyve` of the [Minishift virtualization env](https://docs.openshift.org/latest/minishift/getting-started/setting-up-virtualization-environment.html).

**NOTE: You will need to allocate at least 4GB of RAM to the Docker process, otherwise Ops Manager will be unable to start!**

## Install Kubectl

```bash
brew install kubectl
```

## Install Openshift-CLI 3.9.0

```bash
brew install openshift-cli

# Alternatively, install 3.9.0 directly from GitHub (supports Linux as well)
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

## Install Minishift (optional)

```bash
brew cask install minishift
```

## Install Helm

See the [getting started with Helm in Openshift](https://blog.openshift.com/getting-started-helm-openshift/) page for more details.

```bash
brew install helm

# Alternatively, install a specific version (2.9.0) and add it to your path
cd "${HOME}"
curl -s https://storage.googleapis.com/kubernetes-helm/helm-v2.9.0-darwin-amd64.tar.gz | tar xz
darwin-amd64/helm init --client-only
export PATH=$(pwd)/darwin-amd64:${PATH}
```

# Deploy and configure an Openshift cluster

```bash
../docker/mdbk.sh --openshift --ops-manager
```

# Deploying resources

See the [Deploying resources](./deploying-resources.md) instructions!

## Shutdown cluster

The following script shuts down the Openshift cluster (removing all configurations) and also stops and deletes the Ops Manager docker container.

```bash
../docker/mdbk.sh --openshift --undeploy
```

### Openshift Docker repository

**Prerequisites (required only once):**
1\. Configure localhost as an insecure registry
  Add `127.0.0.1:80` as an insecure registry in `${HOME}/.docker/daemon.json`
2\. Expose the default Docker registry in Openshift
  ```bash
  # Expose the registry (insecure)
  ## https://docs.openshift.com/container-platform/3.9/install_config/registry/securing_and_exposing_registry.html#exposing-the-registry
  oc login -u system:admin
  oc project default
  oc expose service docker-registry --hostname=docker-registry-default.127.0.0.1.nip.io
  export OPENSHIFT_REGISTRY="$(oc get route docker-registry -n default --template='{{ .spec.host }}'):80"

  # Check the registry's health
  curl -sI "http://${OPENSHIFT_REGISTRY}/healthz" | grep '200 OK' || echo "Something went wrong\!"

  # Give the mongdb user, rights to push to the Openshift Docker registry
  ## https://docs.openshift.com/container-platform/3.9/admin_guide/manage_rbac.html#viewing-roles-and-bindings
  oc adm policy add-role-to-user system:registry mongodb
  ```
3\. Build and push the images to the Openshift Docker registry
  ```bash
  # Build the operator and database images
  ../scripts/dev/rebuild-mongodb-enterprise-operator-image.sh
  ../scripts/dev/rebuild-mongodb-enterprise-database-image.sh

  # Login as the mongodb user and then to Docker
  oc login -u mongodb -p mongodb -n mongodb
  docker login -u $(oc whoami) -p $(oc whoami -t) ${OPENSHIFT_REGISTRY}

  # Push the images
  for img in mongodb-enterprise-operator mongodb-enterprise-database; do
      docker tag ${img} ${OPENSHIFT_REGISTRY}/mongodb/${img}:latest
      docker push ${OPENSHIFT_REGISTRY}/mongodb/${img}
  done
  ```

```bash
export operator_repo="mongodb"
export operator_version="latest"
helm del --tiller-namespace tiller --purge mongodb-enterprise >/dev/null 2>&1
helm install --namespace mongodb \
  --set registry.repository="${OPENSHIFT_REGISTRY}/${operator_repo}" \
  --set registry.version="${operator_version}" \
  --set registry.pullPolicy="Never" \
  --name mongodb-enterprise public/helm_chart -f public/helm_chart/values.yaml
```
