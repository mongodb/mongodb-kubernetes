# Compiling, Building and Installing Operator Image #

## Prerequisites ##

* Make sure to checkout this project into the `src/github.com/10gen` folder in the `$GOPATH` directory as described
 [here](https://golang.org/doc/code.html). So if your `$GOPATH` variable points to `/home/user/go` then the project 
 must be checked out into `/Users/user/go/src/github.com/10gen/ops-manager-kubernetes`
* [Go](https://golang.org/doc/install): Go programming language (we use the latest current version which is `1.13.3`)


## Compile Operator ##

```
./scripts/build/build_operator
```

This script will **only compile** operator but not create an image.

Note, if your ssh key is protected by passphrase `dep ensure` won't show any prompt and it will just [hang](https://github.com/golang/dep/issues/1726). In order to cache the git credential you can follow this [tutorial](https://help.github.com/articles/generating-a-new-ssh-key-and-adding-it-to-the-ssh-agent/#adding-your-ssh-key-to-the-ssh-agent).

### Create Required Container Images ###

The operator needs 2 different container images:

* mongodb-enterprise-operator: which is the actual operator running in a Pod
* mongodb-enterprise-database: which is the container running automation-agent binary on each of the Kubernetes pods. 
It is responsible for managing mongod process locally

```
./scripts/dev/rebuild-mongodb-enterprise-database-image.sh
./scripts/dev/rebuild-mongodb-enterprise-operator-image.sh
```

Note, that `rebuild-mongodb-enterprise-operator-image.sh` will **both compile operator binary and build docker image** (it will
be pushed to Minikube Docker registry)

### Operator Installation ###

We use `helm` for flexible installation of images to Kubernetes. Check the [helm docs](docs/helm.md) about some possible
caveats of installing helm to cluster with RBAC enabled. Skip all sections starting from **Creating a Mongodb Namespace**

Use the following commands to drop/deploy Operator to Minikube:

``` bash
helm del --purge mongodb-enterprise
helm install --tiller-namespace "tiller" --namespace "mongodb" --name mongodb-enterprise \
    public/helm_chart -f public/helm_chart/values.yaml \
    --set operator.version="latest" \
    --set registry.pullPolicy="Never"
``` 

This will create 4 new resources in Kubernetes. The `mongodb-enterprise-operator` application will watch the 
creation/modification of any new MongoDB Kubernetes objects (standalones, replica sets, sharded clusters) and 
reflect this in managed Kubernetes pods and OpsManager deployment configuration.
