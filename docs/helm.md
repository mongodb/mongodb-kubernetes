# MongoDB Operator Helm Chart (dev builds) #

This repository will allow you to install the MongoDB Kubernetes
Operator in your cluster by using [Helm](https://github.com/kubernetes/helm) from private `quay.io` repository
which is used for storing images built from master branch.

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

Running Helm on `minikube` v1.10 cluster or on cluster with RBAC enabled (`kops`, `OpenShift`) will
result in **permission errors** (for example when `Helm` tries to create a Kubernetes `Role`).
To avoid that you should use the following installation instructions instead to create a service account for `Helm Tiller`
and assign `cluster-admin` role to him:

``` bash
kubectl create serviceaccount --namespace kube-system tiller
kubectl create clusterrolebinding tiller-cluster-rule --clusterrole=cluster-admin --serviceaccount=kube-system:tiller
helm init --service-account tiller
```


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
$ kubectl get secrets -n mongodb
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

(Don't forget to follow the "Helm Install" instructions first!)

``` bash
$ git clone git@github.com:10gen/ops-manager-kubernetes.git
$ cd ops-manager-kubernetes
$ helm install public/helm_chart -f config/helm/values-quay.yaml --set imagePullSecrets=<user>-pull-secret --name mongodb-enterprise
```

This will create a helm release named `mongodb-enterprise` in Kubernetes.
In that last line you will set the `imagePullSecrets` to the value you
got in the last section; something like `<user>-pull-secret` changing
`<user>` to your own name.

To remove the helm release use the following instruction:

``` bash
$ helm del --purge mongodb-enterprise
```

## Confirm everything is working ##

You can check with the following command:

``` bash
kubectl get pods -n mongodb
NAME                                           READY     STATUS    RESTARTS   AGE
mongodb-enterprise-operator-85594475fb-jjj9v   1/1       Running   0          11s
```

It might take a few seconds for `STATUS` to change from
`ContainerCreating` to `Running`. 

If container is in `Running` status then additionally it may be useful to check logs:

``` bash
kubectl logs -f deployment/mongodb-enterprise-operator -n mongodb
```

The good output should look similar to following:

```
2018-06-07T21:32:22.856Z	INFO	ops-manager-kubernetes/main.go:78	Operator environment: dev
2018-06-07T21:32:22.859Z	INFO	ops-manager-kubernetes/main.go:44	Ensuring the Custom Resource Definitions exist	{"crds": ["mongodbreplicaset", "mongodbstandalone", "mongodbshardedcluster"]}
2018-06-07T21:32:24.410Z	INFO	ops-manager-kubernetes/main.go:57	Starting watching resources for CRDs just created
``` 

## Troubleshooting ##

If pod doesn't have `Running` status the first action should be to check the detailed status of pod
 
``` bash
kubectl describe pod mongodb-enterprise-operator-85594475fb-jjj9v -n mongodb
```
Check the `Events` block to see detailed description of the error. Verify the urls of Operator image 
(`Containers.mongodb-enterprise-operator.Image`) and Database image (`MONGODB_ENTERPRISE_DATABASE_IMAGE`). 
Check that `IMAGE_PULL_SECRETS` has the correct secret name.

If you still have problems, please ask in `#opsmanager-kubernetes` Slack channel.

## Development Hints

Use other `values-*` files in `config/helm` directory to use images from other locations. `values-dev.yaml` points to
development version in AWS ECR repository and can be used if the cluster is deployed to AWS (as no image pull secret is required)

Use `values-local.yaml` file to use local Minikube Docker registry. 

As always it's possible to create a custom configuration file starting with `my-` - it won't be tracked by Git.
