# Ops Manager Operator #

This is a Kubernetes operator (https://coreos.com/operators/) to work
with Ops Manager and Kubernetes clusters. It allows to easily add new
MongoDB deployments (standalones, replica sets, sharded clusters) to your Kubernetes cluster, configure them (modify, scale up/down, remove) and to manage them from your
Ops Manager installation. This provides combined power of Kubernetes (native scheduling of applications to nodes, scaling, fault tolerance etc) with Ops Manager capabilities (monitoring, backup, upgrades etc)

## High-level

The high-level picturefor the process of installing Mongodb deployment into Kubernetes cluster is as follows:
* admin creates the `om-operator` Kubernetes Deployment which contains the operator application. This is a one-time operation.
* admin creates custom MongoDB objects in Kubernetes (`MongoDbStandalone`, `MongoDbReplicaSet`, `MongoDbShardedCluster`). For example is `kubectl apply -f my-replicaset.yaml`
* `om-operator` watches these changes and applies them to different participants:
  * creates the Kubernetes StatefulSet containing containers with automation agent binaries. They will be responsible for installation and managing local mongod process.
  * applies changes to the OpsManager automation config using public API. So the deployment object (OM replica set for example) will be created. These changes will be propagated back to the automation agents sitting in the pods and they will do all dirty work of downloading and launching MongoDB binaries locally in the same container
  
The update process follows the same approach in general except for no new objects are created in Kubernetes and OpsManager but current existing ones are updated. The example of such modification is scaling down/up of a replica set which will remove/add pods to the StatefulSet and remove/add members to the replica set in Ops Manager   


## Installation ##
### Prerequisites
* Make sure to checkout this project into the `src` folder in the `$GOPATH` directory as described [here](https://golang.org/doc/code.html). So if your `$GOPATH` variable points to `/home/user/go` then the project must be checked out into `/Users/user/go/src/github.com/10gen/ops-manager-kubernetes`
* Prepare the Kubernetes environment (Install [Minikube](https://kubernetes.io/docs/getting-started-guides/minikube/) for quick start)
* [Install Go](https://golang.org/doc/install) (we use the latest current version which is `1.9.4`)
### Compile Operator ###

```
dep ensure
./codegen.sh
CGO_ENABLED=0 GOOS=linux go build -o om-operator
```
> Note that currently compilation is failing as there is version mismatch in kubernetes API version used by operator-kit library and ops-manager kubernetes. This can be fixed by manual editing of resource.go file (see `op-kit-patch.diff` file in the root of the project)

### Create Required Container Images ###

The operator needs 2 different container images:

* om-operator: which is the actual operator running in a Pod
* automation-agent: which is the container running automation-agent binary on each of the Kubernetes pods. It is responsible for managing mongod process locally


```
$ eval $(minikube docker-env)
$ docker build -t om-operator:0.1 .
$ docker build automation-agent -t ops-manager-agent -f automation-agent/Dockerfile

```

### Operator Installation ###

To install the ops-manager operator you'll only need to execute the
following:

    $ kubectl create -f om-operator.yaml
    clusterrole "om-operator" created
    serviceaccount "om-operator" created
    clusterrolebinding "om-operator" created
    deployment "om-operator" created

This will create 4 new resources in Kubernetes. The `om-operator` application will watch the creation/modification of any new MongoDB Kubernetes objects (standalones, replica sets, sharded clusters) and reflect this in managed Kubernetes pods and OpsManager deployment configuration.   

## Create your First Managed MongoDB ReplicaSet ##

You will only need a working Ops Manager installation (you can use [mci](https://mci.mms-build.10gen.cc) to provision an OpsManager instance). After having
installed Ops Manager please get the following data and configuration
parameters from it:

* User login with sufficient privileges
* Public API Key
* Group ID
* Agent API Key
* Base URL

> Note that in addition to public API key generation you need to whitelist the Kubernetes cluster IP in OpsManager

Now with this data, copy the `samples/om-config-map-sample.yaml` to `samples/my-om-config-map.yaml` and edit it.
Copy the `samples/om-replica-set-sample.yaml` file to `samples/my-replica-set-sample.yaml` and edit it.

> files with path `samples/my-` are ignored by git

Create the config-map and replica set objects in Kubernetes:

    $ kubectl apply -f my-om-config-map.yaml
    configmap "ops-manager-config" configured
    $ kubectl apply -f my-replica-set.yaml
    service "alpha-service" created
    mongodbreplicaset "liffey" created

After executing this command you should have a working replica set that you can manage from Ops Manager.
