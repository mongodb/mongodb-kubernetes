# MongoDB Operator Helm Chart #

This repository will allow you to install the MongoDB Kubernetes
Operator in your cluster by using [Helm](https://github.com/kubernetes/helm).

## Requirements ##

For this operator to work you need an existing Ops Manager or Cloud
Manager installation, as they provide the administrative logic and
automation. You will also need credentials for the `quay.io` private
repository. Ask the Ops Manager team to get your invitation.

### Helm Install ###

Make sure you have installed Helm in your cluster. Please follow the
instructions [here](https://github.com/kubernetes/helm#install) on how
to do it. Please read [this document](https://blog.openshift.com/getting-started-helm-openshift/)
if you want to install `Helm` in RedHat Openshift.

Please review the **"Additional Notes"** section at the end of the
document if you find troubles installing `Helm` or if you install Helm on the cluster with **RBAC enabled** (e.g. 
the cluster created by `kops` or `OpenShift` cluster have RBAC support by default)

## Creating a Mongodb Namespace ##

A namespace is a Kubernetes encapsulation that allow us to scope our
project. We will create everything inside this namespace which we are
going to call `mongodb`.

    kubectl create namespace mongodb

## Adding Quay.io Private Repo Credentials ##

The operator and database images are stored in the
`quay.io/mongodb-enterprise-internal` private repository. You need to
create an account in `quay.io` and request read access to this private
repository. After access has been granted to your user, you need to
look for the necessary configuration by visiting `quay.io`:

1. Click on your username on the top right corner.
2. Click on `Account Settings`
3. Under `Docker CLI Password` click on `Generate Encrypted Password`
4. You will have to provide your password once.
5. A dialog with several options will appear: select "Kubernetes"
6. Follow instructions 1) and 2) from that screen. In instructions 2)
   make sure you use `mongodb` instead of `NAMESPACEHERE`.

After you have done this, there will be a new `Secrets` resource in
your Kubernetes cluster/namespace that will have the required
credentials to access the private image repository.

Now you need to get the name of the `Secrets` object that was created
for you:

``` bash
$ kubectl get secrets --namespace=mongodb
NAME                                      TYPE                                  DATA      AGE
default-token-pzdsp                       kubernetes.io/service-account-token   3         1m
mongodb-enterprise-operator-token-7l78b   kubernetes.io/service-account-token   3         1m
<user>-pull-secret                        kubernetes.io/dockerconfigjson        1         9s
```

There should be one secret (probably the last one) that has a name
similar to `<user>-pull-secret`, in which, `<user>` should be replaced
by your `quay.io` username. Write this secret's name down because we
will use it in the next step.


## Install Chart ##

(Don't forget to follow the "Install Helm" instructions first!)

``` bash
$ git clone git@github.com:10gen/ops-manager-kubernetes.git
$ helm install ops-manager-kubernetes/helm --set imagePullSecrets=<user>-pull-secret
```

In that last line you will set the `imagePullSecrets` to the value you
got in the last section; something like `<user>-pull-secret` changing
`<user>` to your own name.

## Confirm everything is working ##

You can check with the following command:

``` bash
kubectl get pods --namespace mongodb
NAME                                           READY     STATUS    RESTARTS   AGE
mongodb-enterprise-operator-85594475fb-jjj9v   1/1       Running   0          11s
```

It might take a few seconds for `STATUS` to change from
`ContainerCreating` to `Running`. If instead the status is
`ImageErrPull` it means we have made some mistake in the
process. Follow the instructions again or ask in
`#opsmanager-kubernetes` Slack channel.

## Development Hints

Use the following form to drop/create the Helm release with one command (otherwise Helm will generate new release each time). 
Also providing additional configuration using `-f` flag will allow to override default settings provided by `values.yaml`

```bash
# This will install the operator and database local images
ops-manager-kubernetes$ helm delete --purge om-operator; helm install helm -f helm/env/values-local.yaml --name om-operator
```

As always it's possible to create a custom configuration file starting with `my-` - it won't be tracked by Git.


### Additional Notes ###

Running Helm on `minikube` v1.10 cluster or on cluster with RBAC enabled (`kops`, `OpenShift`) will 
sometimes give you permission errors (for example when Helm tries to create a Kubernetes `Role`). 
To avoid that you can use the following install instructions instead to create a service account for `Helm Tiller`:

``` bash
kubectl create serviceaccount --namespace kube-system tiller
kubectl create clusterrolebinding tiller-cluster-rule --clusterrole=cluster-admin --serviceaccount=kube-system:tiller
helm init --service-account tiller
```
