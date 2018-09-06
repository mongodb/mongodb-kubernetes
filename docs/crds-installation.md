# New Operator Namespace Isolated Mode

Before version 0.3, Custom Resource Definitions (`CRD`s) did not
require installation, instead, they were installed programmatically by
the operator code. This design had consequences, most importantly, the
Operator required elevated privileges in the Cluster to install
the `CRD`s (a `ClusterRole` and related), and kept working in this
elevated privileged mode to listen for changes on any namespace.

Another consequence of this design is that the operator was watching
for Custom Objects in every `Namespace`, and there was not way of
isolating an operator instance to work on one or a given set of
`Namespaces`. This has security implications, the operator would be
able to "see" `Secret`s and `ConfigMap`s for every team for any
`Namespace`.

We have received enough feedback from Customer to realize that this
design was flawled. Also, we are more experienced with the technology
and we have started making more informed decisions.

In 0.3 release we are changing the way the `CRD`s are installed and
how the operator should be installed to act on these new type of
resources.

## Installation of CRDs

The first step in installing the operator is to create the `CRD`, for
this purpose we have provided a `crds.yaml` file that needs to be
applied as usual:

    kubectl apply -f crds.yaml

This will create 3 `CustomResourceDefinition` objects:

* MongoDbStandalone
* MongoDbReplicaSet
* MongoDbShardedCluster

Each one of them corresponds to one of the main deployment objects in
Mongodb.

## Operator Installation

Now you can proceed to install the operator. For testing purposes, it
should be as easy as to execute the following command:

    kubectl apply -f mongodb-enterprise.yaml

This will create a series of Kuberentes objects, namely:

* A `Namespace` object where the operator and Mongodb deployments will reside.
* A `Role`, `RoleBinding` and `ServiceAccount` objects
* A `Deployment` object, the actual Operator

You can find more information about Role-Based Access Control
[here](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
and why all these objects are important for the installation.

Please note, that this file is meant for testing purposes only. We
recommend you read each one of the Resource definitions in the
`mongodb-enterprise.yaml` file and adjust it to your needs. 

After the operator has been installed it will watch, by default, any
Custom Object created in the same `Namespace` the operator itself was
installed.

If you want the operator to "watch" other namespaces, you will have to
install it in every `Namespace` you want. Each operator installation
will be isolated and different operators will not know about the
others. The only resource they will share will be the `CRD`s, which
reside at Cluster level.

## Operator Update From 0.2 or Previous Version

If you already have a version 0.2 operator running in your cluster. We
recommend you remove it by deleting the `Deployment` corresponding to
it. Be careful to not remove the `Namespace` you created the operator
into, because it might contain MongoDb deployment objects you don't
want to lose.

To only remove the operator `Deployment` you should do:

    kubectl -n <NAMESPACE> delete deployment/mongodb-enterprise-operator

We also advise you to remove any remaining `ClusterRole`s,
`RoleBinding`s and `ServiceAccount`s you don't need anymore.

    kubectl delete clusterrole/mongodb-enterprise-operator
    kubectl delete rolebinding/mongodb-enterprise-operator
    kubectl delete serviceaccount/mongodb-enterprise-operator

At this point you will have a clean Cluster where to install your
operator.

Also at this point, you will probably have one or many `Namespace`s
with Custom Objects in it, if you want to manage these MongoDb custom
resources, you will have to install the Operator in each one of these
`Namespace`s following the "Installation Instructions" in the previous
section.
