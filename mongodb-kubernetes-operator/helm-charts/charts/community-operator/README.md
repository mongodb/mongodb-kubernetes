# MongoDB Community Kubernetes Operator Helm Chart

A Helm Chart for installing and upgrading the [MongoDB Community
Kubernetes Operator](https://github.com/mongodb/mongodb-kubernetes-operator).

## Prerequisites

If required, you can install the Custom Resource Definitions [Helm
Chart](../community-operator-crds/) separately or as a dependency of this Chart.

If the `community-operator-crds` Helm chart has been installed already, or if you
don't want to install the CRDs (because you have already installed them), then
you need to pass `--set community-operator-crds.enabled=false`, when
installing the Operator.

## Installing Community Operator

You can install the MongoDB Community Operator easily with:

``` shell
helm install community-operator mongodb/community-operator
```

This will install `CRD`s and Community Operator in the current namespace
(`default` by _default_). You can pass a different namespace with:

``` shell
helm install community-operator mongodb/community-operator --namespace mongodb [--create-namespace]
```

To install the Community Operator in a namespace called `mongodb` with the
optional `--create-namespace` in case `mongodb` didn't exist yet.


## Deploying a MongoDB Replica Set

The Community Operator will be watching for resources of type
`mongodbcommunity.mongodbcommunity.mongodb.com`; you can quickly install
a sample Mongo Database with:

``` shell
kubectl apply -f https://raw.githubusercontent.com/mongodb/mongodb-kubernetes-operator/master/config/samples/mongodb.com_v1_mongodbcommunity_cr.yaml [--namespace mongodb]
```

- _Note: Make sure you add the `--namespace` option when needed._
- _Note 2: A new user will be created with a generic password. Make sure this is
  only used for testing purposes._

After a few minutes you will have a 3-member MongoDB Replica Set installed in
your cluster, that you can check with:

``` shell
$ kubectl get mdbc
NAME              PHASE     VERSION
example-mongodb   Running   4.2.6
```

## Connecting to MongoDB from a Client Application

The Operator will create a `Secret` object, _per user_, created as part of the
deployment of the MongoDB resource. Each `Secret` will contain a _Connection
String_ that can be mounted into a client application to connect to this MongoDB
instance.

The name of this `Secret` object follows the convention[^1]:

- `<mongodb-resource-name>-<database>-<username>`.

[^1]: Please note that the MongoDB `username` should comply with
    [DNS-1123](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-label-names)
    for the Operator to be able to create this Secret. This is a known issue
    with the Community Operator.

In our example, the above `kubectl apply` command will create a MongoDB resource
with name `example-mongodb`, with a user `my-user` on the Database `admin`. The
resulting `Secret` will be named:

- `example-mongodb-admin-my-user`

This `Secret` object will contain the following attributes:

- `connectionString.standard`
- `connectionString.standardSrv`
- `username`
- `password`

A client application will be able to connect using the `connectionString`
attributes or the `username` and `password` ones.
