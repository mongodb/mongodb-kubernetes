# MongoDB Atlas Operator Helm Chart

A Helm chart for installing and upgrading the [MongoDB Atlas
Operator](https://github.com/mongodb/mongodb-atlas-kubernetes).

## Prerequisites

If required, you can install the Atlas Custom Resource Definitions [Helm
Chart](../atlas-operator-crds/) separately or as a dependency of this Chart.

If the `atlas-operator-crds` Helm chart has been installed already, or if you
don't want to install the CRDs (because you have already installed them), then
you need to pass `--set mongodb-atlas-operator-crds.enabled=false`, when
installing the Operator.

### Watching over all Namespaces

This will install the Operator in _Cluster wide mode_. The Operator will watch
over all the namespaces in the Kubernetes cluster.

```shell
helm install atlas-operator mongodb/mongodb-atlas-operator \
    --namespace=atlas-operator \
    --create-namespace
```

### Watching over same Namespace

This installation mode will restrict the Operator to watch over resources created
in the same namespace the Operator is installed.

```shell
helm install atlas-operator mongodb/mongodb-atlas-operator \
    --namespace=atlas-operator \
    --set watchNamespaces=atlas-operator \
    --create-namespace
```

### Watching over multiple Namespaces

This installation mode will allow the Operator to watch over resources created in the 
namespaces specified by the watchNamespaces parameter.

```shell
helm install atlas-operator mongodb/mongodb-atlas-operator \
    --namespace=atlas-operator \
    --set watchNamespaces="{ns1,ns2}" \
    --create-namespace
```

Note: Same thing can be achieved via _values.yaml_ as well.
```shell
watchNamespaces:
  - ns1
  - ns2
```

### Watching over all Namespaces with Global Atlas configuration

In this mode the Operator will be installed in _Cluster wide mode_ with [Global
Atlas configuration](https://docs.atlas.mongodb.com/reference/atlas-operator/configure-ak8so-access-to-atlas/).

```shell
helm install atlas-operator mongodb/mongodb-atlas-operator \
    --namespace=atlas-operator \
    --create-namespace \
    --set globalConnectionSecret.publicApiKey=<the_public_key> \
    --set globalConnectionSecret.privateApiKey=<the_private_key> \
    --set globalConnectionSecret.orgId=<the_org_id>
```

### Upgrading the Operator:

```
helm upgrade atlas-operator mongodb/mongodb-atlas-operator
```

## Creating Atlas Resources

After the `atlas-operator` Helm Chart has been installed, you can proceed to
[Atlas Cluster](../atlas-deployment) Helm Chart to create your first Atlas
database.
