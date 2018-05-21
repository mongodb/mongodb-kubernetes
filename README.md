# Ops Manager Operator #

This is a Kubernetes operator (https://coreos.com/operators/) to work
with Ops Manager and Kubernetes clusters. It allows to easily add new
MongoDB deployments (standalones, replica sets, sharded clusters) to your Kubernetes cluster, configure them (modify, scale up/down, remove) and to manage them from your
Ops Manager installation. This provides combined power of Kubernetes (native scheduling of applications to nodes, scaling, fault tolerance etc) with Ops Manager capabilities (monitoring, backup, upgrades etc)

## For Users only
If you want just to **run** the Ops Manager Kubernetes operator built from `master` - you don't need to compile/build artifacts and you can follow the [Helm instructions](/helm/README.md) to install the existing operator image to your Kubernetes cluster.
You can create a local Kubernetes cluster easily using [Minikube](https://kubernetes.io/docs/getting-started-guides/minikube/)
 
Otherwise follow the next instructions to find out how to build and compile artifacts and deploy them to Kubernetes
    
## High-level

The high-level picture for the process of installing Mongodb deployment into Kubernetes cluster is as follows:
* admin creates the `mongodb-enterprise-operator` Kubernetes Deployment which contains the operator application from config `om-operator.yaml`. This is a one-time operation.
* admin creates custom MongoDB objects in Kubernetes (`MongoDbStandalone`, `MongoDbReplicaSet`, `MongoDbShardedCluster`). For example is `kubectl apply -f my-replicaset.yaml`
* `om-operator` watches these changes and applies them to different participants:
  * creates the Kubernetes StatefulSet containing containers with automation agent binaries. They will be responsible for installation and managing local mongod process.
  * applies changes to the OpsManager automation config using public API. So the deployment object (OM replica set for example) will be created. These changes will be propagated back to the automation agents sitting in the pods and they will do all dirty work of downloading and launching MongoDB binaries locally in the same container

The update process follows the same approach in general except for no new objects are created in Kubernetes and OpsManager but current existing ones are updated. The example of such modification is scaling down/up of a replica set which will remove/add pods to the StatefulSet and remove/add members to the replica set in Ops Manager


## Installation ##
### Prerequisites

* Make sure to checkout this project into the `src/github.com/10gen` folder in the `$GOPATH` directory as described [here](https://golang.org/doc/code.html). So if your `$GOPATH` variable points to `/home/user/go` then the project must be checked out into `/Users/user/go/src/github.com/10gen/ops-manager-kubernetes`
* [Minikube](https://kubernetes.io/docs/getting-started-guides/minikube/): Easy to install Kubernetes cluster.
* [Go](https://golang.org/doc/install): Go programming language (we use the latest current version which is `1.9.4`)
* [dep](https://github.com/golang/dep): Go dependency tool

*Hint:* as all Kubernetes objects are created in `mongodb` namespace it makes sense to set this namespace as the default one
in minikube using the command `kubectl config set-context $(kubectl config current-context) --namespace=mongodb`. After
this all `kubectl` commands will work for the resources in this namespace by default unless you override it using `-n <other_namespace>` syntax
### Compile Operator ###

```
dep ensure
./codegen.sh
CGO_ENABLED=0 GOOS=linux go build -i -o om-operator
```

Note, if your ssh key is protected by passphrase `dep ensure` won't show any prompt and it will just [hang](https://github.com/golang/dep/issues/1726). In order to cache the git credential you can follow this [tutorial](https://help.github.com/articles/generating-a-new-ssh-key-and-adding-it-to-the-ssh-agent/#adding-your-ssh-key-to-the-ssh-agent).

### Create Required Container Images ###

The operator needs 2 different container images:

* mongodb-enterprise-operator: which is the actual operator running in a Pod
* mongodb-enterprise-database: which is the container running automation-agent binary on each of the Kubernetes pods. It is responsible for managing mongod process locally


```
./rebuild-operator.sh
./rebuild-agent.sh

```

### Operator Installation ###

To install the ops-manager operator you'll only need to execute the
following:

    $ kubectl create -f om-operator-local.yaml
    clusterrole "om-operator" created
    serviceaccount "om-operator" created
    clusterrolebinding "om-operator" created
    deployment "om-operator" created

This will create 4 new resources in Kubernetes. The `om-operator` application will watch the creation/modification of any new MongoDB Kubernetes objects (standalones, replica sets, sharded clusters) and reflect this in managed Kubernetes pods and OpsManager deployment configuration.

## Create your first managed MongoDB ReplicaSet ##

You will only need a working Ops Manager installation (you can use [mci](https://mci.mms-build.10gen.cc) to provision an OpsManager instance). Get the following data and configuration
parameters from it:

* User login with sufficient privileges
* Public API Key
* Group ID
* Base URL

> Note that in addition to public API key generation you need to whitelist the Kubernetes cluster IP in OpsManager

Now with this data, copy the `samples/om-config-map-sample.yaml` to `samples/my-om-config-map.yaml`,
 `samples/om-replica-set-sample.yaml` to `samples/my-replica-set-sample.yaml` and edit them.

> All files in the project starting with `my-` prefix are ignored by git so you can modify them locally at any time

Create the config-map and replica set objects in Kubernetes:

    $ kubectl apply -f my-om-config-map.yaml
    configmap "ops-manager-config" configured
    $ kubectl apply -f my-replica-set.yaml
    service "alpha-service" created
    mongodbreplicaset "liffey" created

After executing this command you should have a working replica set that you can manage from Ops Manager.

## Check database connectivity

After new deployment is created it's always good to check whether it works correctly. To do this you can deploy a small 
`node.js` application into Kubernetes cluster which will try to connect to database, create 3 records there and read all existing ones:

    $ eval $(minikube docker-env)
    $ docker build -t node-mongo-app:0.1 docker/node-mongo-app -f docker/node-mongo-app/Dockerfile
    ....
    Successfully tagged node-mongo-app:0.1

Now copy `samples/node-mongo-app.yaml` to `samples/my-node-mongo-app.yaml` and change the `DATABASE_URL` property in 
`samples/node-mongo-app.yaml` to target the mongodb deployment.
This can be a single url (for standalone) or a list of replicas/mongos instances (e.g. 
`mongodb://liffey-0.alpha-service:27017,liffey-1.alpha-service:27017,liffey-2.alpha-service:27017/?replicaSet=liffey` for replica set or
 `mongodb://shannon-mongos-0.shannon-svc.mongodb.svc.cluster.local:27017,shannon-mongos-1.shannon-svc.mongodb.svc.cluster.local:27017`
 for sharded cluster).
Hostnames can be received form OM deployment page and have the short form `<pod-name>.<service-name>` or full version 
`<pod-name>.<service-name>.mongodb.svc.cluster.local`
After this create a job in Kubernetes (it will run once and terminate):

    $ kubectl delete -f samples/my-node-mongo-app.yaml; kubectl apply -f samples/my-node-mongo-app.yaml
    deployment "test-mongo-app" configured

Reading logs:

    $ kubectl logs -l app=test-mongo-app
    Connected successfully to server
    Collection deleted
    Inserted 3 documents into the collection
    Found the following records
    [ { _id: 5aabda6c9398583e26bf211a, a: 1 },
      { _id: 5aabda6c9398586118bf211b, a: 2 },
      { _id: 5aabda6c939858c046bf211c, a: 3 } ]

## Development Process
### Dev Clusters
We use `kops` utility to provision and manage Kubernetes clusters. We have one shared environment for development `dev.mongokubernetes.com` and each developer can create their own clusters. Usual practice is start from 3 EC2 nodes and extend them to bigger number only if necessary.

More on working with `kops` is [here](docs/aws_kops.md)

### Docker Registry
Docker images are published to Elastic Container Registry `268558157000.dkr.ecr.us-east-1.amazonaws.com` where a specific repository is created for each of namespace/application combinations. For example `dev` versions of agent and operator reside in `268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-database` and `268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-operator`.

More on how to work with ECR is [here](docs/aws_docker_registry.md)

For public images we plan to use `quay.io`

### Workflow
The development workflow is not quite settled yet, there is no easy way of quick changing/redeploying environments using standard scripts, also we plan to use `Helm` for packaging of Kubernetes artifacts configurations. There is a general script for cleaning Kubernetes objects and deploying om-operator there (`restart.sh`) - you can create a copy with prefix `my-` and accomodate it for your purposes - it will be ignored by Git.
