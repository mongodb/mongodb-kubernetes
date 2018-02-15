# Ops Manager Operator #

This is a Kubernetes operator (https://coreos.com/operators/) to work
with Ops Manager and Kubernetes clusters. It allows to easily add new
resources to your Kubernetes installation and to manage them from your
Ops Manager installation.

## Installation ##

### Compile Operator ###

    $ dep ensure
    $ ./codegen.sh

### Create Required Container Images ###

The operator needs 2 different container images:

* om-operator: which is the actual operator running in a Pod
* automation-agent: which is the container that runs the automation
  agent when mongodb is deployed.


```
$ eval $(minikube docker-env)
$ docker build -t operator:0.1 .
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

When executing the `kubectl create` command, 4 new resources will be
added to your Kubernetes installation.

## Create your First Managed MongoDB ReplicaSet ##

You will only need a working Ops Manager installation. After having
installed Ops Manager please get the following data and configuration
parameters from it:

* Email of user with sufficient privileges
* Public API Key
* Group ID
* Agent API Key
* Base URL

Now with this data, complete the sample `om-config-map.yaml`
file. After this has been created, go ahead and create the replica set
with:

    $ kubectl create -f om-resource-sample.yaml
    mongodbreplicaset "my-mongodb-replicaset" created

After executing this command you should have a working replica set,
that you can manage from Ops Manager.
