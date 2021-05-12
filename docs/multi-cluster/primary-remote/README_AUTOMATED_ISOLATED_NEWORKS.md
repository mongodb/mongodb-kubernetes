# Multi Cluster Deployment with Isolated Networks

This guide describes the process to deploy a Replica Set in a multi-cluster
environment, using 3 GCP clusters and Istio.

The end result will be a 7 member Replica Set, deployed across the 3 clusters,
with 3 members in `cluster1`, 2 members in `cluster2` and 2 members in
`cluster3`. The Pods will be deployed using one StatefulSet per cluster. To
satisfy Istio requirements, a Service will have to be created as an entrypoint
for each one of the Pods, this is 7; the Services will have to be created in all
three clusters.

Reference:

* [Install Primary-Remote on different networks](https://istio.io/latest/docs/setup/install/multicluster/primary-remote_multi-network/).


# Deploying Clusters with GCP

We'll try to automate the process as much as possible, to deploy GCP clusters,
we'll use `gcloud` CLI tool. Get it from
[https://cloud.google.com/sdk/gcloud](here).

To create GCP Kubernetes clusters is very easy, we'll start creating 2 clusters
with:

``` shell
gcloud container clusters create cluster1 --machine-type e2-standard-4 --zone europe-west1-c
gcloud container clusters create cluster2 --machine-type e2-standard-4 --zone europe-west1-b
gcloud container clusters create cluster3 --machine-type e2-standard-4 --zone europe-west1-d
```

Here we are using `europe-west1-c` and `europe-west1-b` which is the closest I
have from home. Make sure you use the zones closest to you for testing purposes.
A list of zones can be found with `gcloud compute zones list`.

## Configuring kubectl Contexts

To easily follow the Istio docs, we'll rename clusters to `cluster1` and
`cluster2` with:

``` shell
kubectl config rename-context gke_scratch-kubernetes-team_europe-west1-c_cluster1 cluster1
kubectl config rename-context gke_scratch-kubernetes-team_europe-west1-b_cluster2 cluster2
kubectl config rename-context gke_scratch-kubernetes-team_europe-west1-d_cluster3 cluster3
```

Please note that the *names* of the contexts as created by `gcloud` could be
different.

To check that the clusters were deployed correctly you can do:

``` shell
kubectl get namespaces --context=cluster1
kubectl get namespaces --context=cluster2
kubectl get namespaces --context=cluster3
```

# Istio Installation

Follow [these
instructions](https://istio.io/latest/docs/setup/getting-started/#download) to
install Istio in your system. Make sure the `istioctl` command is added to
`$PATH`.

``` shell
$ istioctl
Istio configuration command line utility for service operators to
debug and diagnose their Istio mesh.

Usage:
  istioctl [command]
...
```

## Configure Trust

To establish trust between clusters we'll deploy a series of TLS certificates to
them. There are detailed instructions on how to do this
[here](https://istio.io/latest/docs/tasks/security/cert-management/plugin-ca-cert/).
Anyway, we have repeated them in simple terms here.

### Generate trusted Certs

Go to the location where istio was installed

``` shell
mkdir -p certs
pushd certs
make -f ../tools/certs/Makefile.selfsigned.mk root-ca
make -f ../tools/certs/Makefile.selfsigned.mk cluster1-cacerts
make -f ../tools/certs/Makefile.selfsigned.mk cluster2-cacerts
make -f ../tools/certs/Makefile.selfsigned.mk cluster3-cacerts
popd
```

### Add certs to each cluster

``` shell
# cluster1
kubectl create namespace istio-system --context cluster1
kubectl create secret generic cacerts --context cluster1 -n istio-system \
      --from-file=certs/cluster1/ca-cert.pem \
      --from-file=certs/cluster1/ca-key.pem \
      --from-file=certs/cluster1/root-cert.pem \
      --from-file=certs/cluster1/cert-chain.pem
kubectl --context cluster1 label namespace istio-system topology.istio.io/network=network1

# cluster2
kubectl create namespace istio-system --context cluster2
kubectl create secret generic cacerts --context cluster2 -n istio-system \
      --from-file=certs/cluster2/ca-cert.pem \
      --from-file=certs/cluster2/ca-key.pem \
      --from-file=certs/cluster2/root-cert.pem \
      --from-file=certs/cluster2/cert-chain.pem

# cluster3
kubectl create namespace istio-system --context cluster3
kubectl create secret generic cacerts --context cluster3 -n istio-system \
      --from-file=certs/cluster3/ca-cert.pem \
      --from-file=certs/cluster3/ca-key.pem \
      --from-file=certs/cluster3/root-cert.pem \
      --from-file=certs/cluster3/cert-chain.pem
```

## Configure `cluster1` as Primary

``` shell
cat <<EOF > cluster1.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster1
      network: network1
EOF

istioctl install --context cluster1 -f cluster1.yaml --skip-confirmation
```

## Configure East-West Gateway in `cluster1`

``` shell
samples/multicluster/gen-eastwest-gateway.sh \
    --mesh mesh1 --cluster cluster1 --network network1 | \
    istioctl --context cluster1  install -y -f -

kubectl --context cluster1 get svc istio-eastwestgateway -n istio-system
```

## Expose Control Panel in `cluster1`

``` shell
kubectl --context cluster1 apply -f samples/multicluster/expose-istiod.yaml
```

## Expose Services in `cluster1`

``` shell
kubectl --context cluster1 apply -n istio-system -f \
    samples/multicluster/expose-services.yaml
```

## Set default network for remote clusters

``` shell
kubectl --context cluster2 label namespace istio-system topology.istio.io/network=network2
kubectl --context cluster3 label namespace istio-system topology.istio.io/network=network3
```

## Enable API Server access to remote clusters

``` shell
istioctl x create-remote-secret \
    --context cluster2 \
    --name=cluster2 | \
    kubectl apply -f - --context cluster1
istioctl x create-remote-secret \
    --context cluster3 \
    --name=cluster3 | \
    kubectl apply -f - --context cluster1
```

## Configure remote clusters

First we need to configure `cluster2`.

``` shell
export DISCOVERY_ADDRESS=$(kubectl --context cluster1 \
    -n istio-system get svc istio-eastwestgateway \
    -o jsonpath='{.status.loadBalancer.ingress[0].ip}')

cat <<EOF > cluster2.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  profile: remote
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster2
      network: network2
      remotePilotAddress: ${DISCOVERY_ADDRESS}
EOF

istioctl install --context cluster2 -f cluster2.yaml --skip-confirmation
```

And similar to the previous one, we configure `cluster3`.

``` shell
cat <<EOF > cluster3.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  profile: remote
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster3
      network: network3
      remotePilotAddress: ${DISCOVERY_ADDRESS}
EOF

istioctl install --context cluster3 -f cluster3.yaml --skip-confirmation
```

## Install East-West Gateway in Remote Clusters

``` shell
samples/multicluster/gen-eastwest-gateway.sh \
    --mesh mesh1 --cluster cluster2 --network network2 | \
    istioctl --context cluster2 install -y -f -

samples/multicluster/gen-eastwest-gateway.sh \
    --mesh mesh1 --cluster cluster3 --network network3 | \
    istioctl --context cluster3 install -y -f -
```

## Expose Services in Remote Clusters

``` shell
kubectl --context cluster2 apply -n istio-system -f \
    samples/multicluster/expose-services.yaml

kubectl --context cluster3 apply -n istio-system -f \
    samples/multicluster/expose-services.yaml
```

After completing these steps, we should have a configured Istio mesh in 3 GCP
clusters.

Next we will deploy a Replica Set in both clusters.

# Deploying a MongoDB ReplicaSet in a Kubernetes Multi-cluster Environment

## Setting Up our Namespace

We'll create a Namespace called `mdb` on both clusters and configure it with
Istio Sidecar injection:

``` shell
kubectl create namespace mdb --context cluster1
kubectl create namespace mdb --context cluster2
kubectl create namespace mdb --context cluster3

kubectl label --context cluster1 namespace mdb \
    istio-injection=enabled
kubectl label --context cluster2 namespace mdb \
    istio-injection=enabled
kubectl label --context cluster3 namespace mdb \
    istio-injection=enabled
```

## Create a Secret holding our Cluster Config

``` shell
kubectl --context=cluster1 create secret generic automation-config-headless --from-file=cluster-config.json -n mdb
kubectl --context=cluster2 create secret generic automation-config-headless --from-file=cluster-config.json -n mdb
kubectl --context=cluster3 create secret generic automation-config-headless --from-file=cluster-config.json -n mdb
```

* Please note that this Cluster Config is configured to work with the provided
settings as is, if anything is changed on the StatefulSet or Services, this file
will have to be manually modified.

## Create Services Pointing at each MongoDB Member Pod

For Istio to work, we need to deploy Services in both clusters.

``` shell
kubectl -n mdb apply -f multi-cluster-services.yaml --context cluster1
kubectl -n mdb apply -f multi-cluster-services.yaml --context cluster2
kubectl -n mdb apply -f multi-cluster-services.yaml --context cluster3
```

This will create 21 Services, 7 on each cluster.

## Create Service Accounts

``` shell
kubectl --context cluster1 create sa mongodb-enterprise-multi-cluster -n mdb
kubectl --context cluster2 create sa mongodb-enterprise-multi-cluster -n mdb
kubectl --context cluster3 create sa mongodb-enterprise-multi-cluster -n mdb
```

## Create StatefulSets controlling the MongoDB Pods

We'll create one StatefulSet per cluster, one with 3 Replica Set members, and
another one with 2.

``` shell
kubectl -n mdb apply -f ../sts-cluster1.yaml --context cluster1
kubectl -n mdb apply -f ../sts-cluster2.yaml --context cluster2
kubectl -n mdb apply -f ../sts-cluster3.yaml --context cluster3
```

# Test

## Check `rs.status()`

We'll test that the Replica Set was started correctly by connecting to it and
getting the `rs.status()` object.

``` shell
# Any Pod in cluster1
kubectl exec rs-cluster1-0 -n mdb --context cluster1 -- /var/lib/mongodb-mms-automation/downloads/bin/mongo --eval 'rs.status()'

# Any Pod in cluster2
kubectl exec rs-cluster2-0 -n mdb --context cluster2 -- /var/lib/mongodb-mms-automation/downloads/bin/mongo --eval 'rs.status()'
```

## Connect to an Interactive `mongo` Shell Session

Let's look for the `PRIMARY` in the status object returned by `rs.status()`, and
connect to that host. In this example, the `PRIMARY` is `rs-cluster1-0`. Because
this Pod is in `cluster1` we need to use `--context cluster`.

``` shell
kubectl exec -it rs-cluster1-0 -n mdb --context cluster1 -- /var/lib/mongodb-mms-automation/downloads/bin/mongo
```

Write something to the database:

``` shell
rs:PRIMARY> use multi_cluster_db
switched to db multi_cluster_db
rs:PRIMARY> db.coll.insert({"msg": "should be replicated accross clusters"})
rs:PRIMARY> exit
```

Now go to any Replica Set member on the *other* cluster:

``` shell
kubectl exec -it rs-cluster1-0 -n mdb --context cluster1 -- /var/lib/mongodb-mms-automation/downloads/bin/mongo
rs:SECONDARY> rs.slaveOk()
rs:SECONDARY> use multi_cluster_db
switched to db multi_cluster_db
rs:SECONDARY> db.coll.find()
{ "_id" : ObjectId("607eb2a9aaed069bd91a9c1b"), "msg" : "should be replicated accross clusters" }
```

# Clean up

## Delete GCP Clusters

Clusters can be removed with `gcloud` tool as well.

``` shell
gcloud container clusters delete cluster1 --zone europe-west1-c
gcloud container clusters delete cluster2 --zone europe-west1-b
gcloud container clusters delete cluster3 --zone europe-west1-d
```

## Delete Kubectl Config

``` shell
kubectl config delete-context cluster1
kubectl config delete-context cluster2
kubectl config delete-context cluster3
```
