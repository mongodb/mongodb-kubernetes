# Ops Manager Operator #

<img align="left" src="https://mongodb-kubernetes-operator.s3.amazonaws.com/img/Leaf-Forest%402x.png">

This is a Kubernetes Operator (https://coreos.com/operators/) to work
with Ops Manager and Kubernetes clusters. It allows you to easily add new
MongoDB deployments (standalones, replica sets, sharded clusters) to your Kubernetes cluster, configure them (modify, scale up/down, remove) and to manage them from your
Ops Manager installation. This provides the combined power of Kubernetes (native scheduling of applications to nodes, scaling, fault tolerance etc) with Ops Manager capabilities (monitoring, backup, upgrades etc)

## High-level

The high-level picture for the process of installing Mongodb deployment into Kubernetes cluster is as follows:
* admin creates the `mongodb-enterprise-operator` Kubernetes Deployment which contains the operator application from config `mongodb-enterprise-operator.yaml`. This is a one-time operation.
* admin creates custom MongoDB objects in Kubernetes (`MongoDB`). For example is `kubectl apply -f my-replicaset.yaml`
* `mongodb-enterprise-operator` watches these changes and applies them to different participants:
  * creates the Kubernetes StatefulSet(s) containing containers with automation agent binaries. They will be responsible for installation and managing local mongod process.
  * applies changes to the Ops Manager automation config using public API. So the deployment object (OM replica set for example) will be created and displayed in Ops Manager Deployment UI. 
These changes will be propagated back to the automation agents sitting in the pods and they will do all the work of downloading and launching MongoDB binaries locally in the same container.

The update process follows the same approach in general except for no new objects are created in Kubernetes and OpsManager but current existing ones are updated. The example of such modification is scaling down/up of a replica set which will remove/add pods to the StatefulSet and remove/add members to the replica set in Ops Manager

## Installation ##

### Prerequisites ###

* Kubernetes cluster. The easiest way is to install [Minikube](docs/minikube.md) locally.
 Another way is to use [Kops](docs/aws_kops.md) to deploy cluster in AWS
 
 *Hint:* as all Kubernetes objects are created in `mongodb` namespace it makes sense to set this namespace as the default one
 using the command `kubectl config set-context $(kubectl config current-context) --namespace=mongodb`. After
 this all `kubectl` commands will work for the resources in this namespace by default unless you override it using `-n <other_namespace>` syntax
 
* Ops Manager / Cloud Manager. To spin up Ops Manager you can use [mci](https://mci.mms-build.10gen.cc). Check more detailed
[instructions](docs/ops-manager.md) about how to enable public API access to Ops Manager.

### Installing Operator from Docker repositories ###

If you want just to **run** the Ops Manager Kubernetes Operator - you don't need to compile/build artifacts as 
you can use prebuilt images.
* To install an official release of Operator please follow the instructions in 
[mongodb-enterprise-kubernetes](https://github.com/mongodb/mongodb-enterprise-kubernetes) repository (which is the public
repository containing helm charts and yaml files to install official version of `mongodb-enterprise-operator`) 
* To install latest image built from master branch you can follow the [Helm instructions for installing dev builds](/docs/helm.md).

### Building and Installing Operator from source code ###

Check the [link](docs/dev/dev-start-guide.md) to learn the development workflow for the Operator.

## Create your first managed MongoDB Object ##

The following data from Ops Manager is necessary to configure Kubernetes Operator to communicate with Ops Manager 
* User login with sufficient privileges
* Public API Key
* Group Name
* Org ID (Optional)
* Base URL

With this you will need to create two Kubernetes objects: a `Project`, which is a logical
agrupation of MongoDB objects in Ops/Cloud Manager, and `Credentials`,
which contain information about the users API Keys needed to perform
actions in Ops/Cloud Manager.

Please refer to [this link](docs/using-credentials.md) for complete
documentation on how to do this.

After this use any of files in `samples/minimal` folder to create a standalone/replica set/sharded cluster.
For example to create replica set execute the following command:

```bash
kubectl apply -f samples/om-replica-set.yaml
```

After invoking this command a set of Kubernetes resources will be created and Ops Manager will display the new replica
set on "Deployment" page

(`samples/minimal` contains the simplest configurations to start with. To see the complete configurations possible with some
 descriptions check the `/samples/extended` folder)

If you don't see a "green" replica set in Ops Manager then you need to check [troubleshooting](docs/troubleshooting.md) 
page for steps that should be taken to diagnose the problem.

## Development

### Getting started
Please see the [starting guide](docs/dev/dev-start-guide.md).

### Dev Clusters
We use `kops` utility to provision and manage Kubernetes clusters. We have one shared environment for development 
`dev.mongokubernetes.com` and each developer can create their own clusters. Usual practice is start from 3 EC2 nodes 
and extend them to bigger number only if necessary.

More on working with `kops` is [here](docs/aws_kops.md)

### Docker ECR Registry
Docker images are published to Elastic Container Registry `268558157000.dkr.ecr.us-east-1.amazonaws.com` where a 
specific repository is created for each of namespace/application combinations. For example `dev` versions of agent and 
operator reside in `268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-database` and 
`268558157000.dkr.ecr.us-east-1.amazonaws.com/dev/mongodb-enterprise-operator`.

More on how to work with ECR is [here](docs/dev/aws_docker_registry.md)

We also use `quay.io` private and public repositories

# Release process

The release process is described [here](docs/release.md).

