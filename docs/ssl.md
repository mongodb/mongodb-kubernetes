# How to Use SSL Issued Certificates Locally #

The certs needed by `mongod` and `mongo` to be able to communicate are
simple `pem` files that include a certificate and a key.


## Creating a Replica Set to put the Kube CA to work

In order to create them, by using the SSL issueing capabilities of
Kubernetes, just create a *secure* Replica Set:

``` yaml
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: my-secure-rs
  namespace: test-namespace
spec:
  members: 3
  version: 4.0.6
  type: ReplicaSet

  project: my-project
  credentials: my-credentials

  persistent: false

  additionalMongodConfig:
    net:
      ssl:
        mode: "requireSSL"
```

After applying this new Replica Set in your cluster, the operator will
prepare 3 (1 per Pod) `CertificateSigningRequest` for the Kubernetes
CA to Issue. They will probably not be issued inmediatelly because
they need to be approved:


``` bash
$ kubect get csr
NAME                                 AGE       REQUESTOR                                                               CONDITION
my-secure-rs-0.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Pending
my-secure-rs-1.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Pending
my-secure-rs-2.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Pending
```

## Approving the new Certificates


``` bash
$ kubectl approve certificate my-secure-rs-{0,1,2}.test-namespace
certificatesigningrequest.certificates.k8s.io/my-secure-rs-0.test-namespace approved
certificatesigningrequest.certificates.k8s.io/my-secure-rs-1.test-namespace approved
certificatesigningrequest.certificates.k8s.io/my-secure-rs-2.test-namespace approved
```

If we check the certs again we'll see that they are approved:

``` bash
$ kubect get csr
NAME                                 AGE       REQUESTOR                                                               CONDITION
my-secure-rs-0.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Issued,Approved
my-secure-rs-1.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Issued,Approved
my-secure-rs-2.test-namespace        30m       system:serviceaccount:test-namespace:mongodb-enterprise-operator        Issued,Approved
```

## Copy the certficates from the Pods

``` bash
kubectl cp "${NAMESPACE}/my-secure-rs-0:/mongodb-automation/server.pem" server0.pem
kubectl cp "${NAMESPACE}/my-secure-rs-1:/mongodb-automation/server.pem" server1.pem
kubectl cp "${NAMESPACE}/my-secure-rs-2:/mongodb-automation/server.pem" server2.pem
kubectl cp "${NAMESPACE}/my-secure-rs-2:/var/run/secrets/kubernetes.io/serviceaccount/..data/ca.crt" ca.crt
```

Now you'll have 3 certificates with names `server0.pem`, `server1.pem`
and `server2.pem`, and the certificate for the Kube CA as `ca.crt`.

The `commonName` of these certs is set to the FQDN of the Pod they
were created for, in this case:

* my-secure-rs-0.my-secure-rs-svc.test-namespace.svc.cluster.local
* my-secure-rs-1.my-secure-rs-svc.test-namespace.svc.cluster.local
* my-secure-rs-2.my-secure-rs-svc.test-namespace.svc.cluster.local

And `mongod` will not allow us to use them anywhere else, unless
`mongod` thinks that it is actually running in a `host` with that
name.

For this test we'll run a Replica Set locally so we'll have to prepare
your host a bit for `mongod` to feel at home.1

## Starting a Replica Set with the new certs

### There's not place like 127.0.0.1 (configuring /etc/hosts)

We'll make the `commonNames` to resolve to our own computer with the
old `/etc/hosts` trick.

``` bash
echo "127.0.0.1 my-secure-rs-0.my-secure-rs-svc.test-namespace.svc.cluster.local" | sudo tee --append /etc/hosts
echo "127.0.0.1 my-secure-rs-1.my-secure-rs-svc.test-namespace.svc.cluster.local" | sudo tee --append /etc/hosts
echo "127.0.0.1 my-secure-rs-2.my-secure-rs-svc.test-namespace.svc.cluster.local" | sudo tee --append /etc/hosts
```

### Running 3 Mongod

The following script will start 3 `mongod` instances with SSL, each
one of them reading a different `server?.pem` file and preparing them
for configuring a Replica Set.

``` bash
    echo "Starting mongods with SSL options"
    mkdir db{0,1,2}
    for i in $(seq 0 1 2); do
        mongod --dbpath "db${i}/" --logpath "db${i}/mongod.log" --port "3700${i}" --replSet "secure0" \
               --sslMode requireSSL --sslPEMKeyFile "server${i}.pem" \
               --fork
    done
```

### Configuring the Replica Set

There is one configuration we must execute, this is, the
`rs.initiate()` command, which mongodb will use to initiate a Replica
Set for this particular group of Mongods.

``` bash
    rsconfig='rs.initiate( {_id : "secure0", members: [ { _id: 0, host: "localhost:37000" }, { _id: 1, host: "localhost:37001" }, { _id: 2, host: "localhost:37002" }] })'
    mongo --host "my-secure-rs-0.my-secure-rs-svc.test-namespace.cluster.local" --port 37000 --ssl --sslCAFile "ca.crt" --eval "${rsconfig}"
    echo "wait until mongodb replica set has finished configuring"
    sleep 5
    mongo --host "my-secure-rs-1.my-secure-rs-svc.test-namespace.cluster.local" --port 37001 --ssl --sslCAFile "ca.crt" --eval "rs.slaveOk()"
```

### Connecting with a member

To connect to a member you must specify the `--host` parameter and
this needs to be the one that that particular `mongod` daemon used to
start with the `--sslPEMKeyFile` that corresponded to that same `host`.

To connect to the first one:

    mongo --host "my-secure-rs-0.my-secure-rs-svc.test-namespace.cluster.local" --port 37000 --ssl --sslCAFile "ca.crt"


To connect to any of the others:

    mongo --host "my-secure-rs-1.my-secure-rs-svc.test-namespace.cluster.local" --port 37001 --ssl --sslCAFile "ca.crt"
    mongo --host "my-secure-rs-2.my-secure-rs-svc.test-namespace.cluster.local" --port 37002 --ssl --sslCAFile "ca.crt"
