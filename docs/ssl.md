# TLS Documentation #

This is a generic document on how to achieve several TLS related tasks, from testing to debugging.

# Getting information about the Certificates #

The certificates can be created automatically by Kubernetes CA. The
certificate itself is mounted into a Pod with its private key file
contained in the same `pem` file. Information about the certificate
can be obtained by using the following command:

    $ openssl x509 -in /mongodb-automation/server.pem -text

This will result in something like the following output:

```
Certificate:
    Data:
        Version: 3 (0x2)
        Serial Number:
            4d:e6:be:fb:19:5c:ba:13:4d:ae:02:af:d4:3d:36:3e:ce:de:01:a2
    Signature Algorithm: sha256WithRSAEncryption
        Issuer: CN=openshift-signer@1551349538
        Validity
            Not Before: Apr 19 13:57:00 2019 GMT
            Not After : Apr 18 13:57:00 2020 GMT
        Subject: C=US, ST=NY, L=NY, O=mongodb, OU=MongoDB Kubernetes Operator, CN=<mdb-name>-<pod-index>
        Subject Public Key Info:
                Public Key Algorithm: rsaEncryption
                Public-Key: (4096 bit)
                Modulus:
                    ...
                    ...
                    ...
                Exponent: 65537 (0x10001)

        X509v3 extensions:
            X509v3 Key Usage: critical
                Digital Signature, Key Encipherment
            X509v3 Extended Key Usage:
                TLS Web Server Authentication, TLS Web Client Authentication
            X509v3 Basic Constraints: critical
                CA:FALSE
            X509v3 Subject Key Identifier:
                9B:9D:CF:93:CA:FB:6A:52:E4:7C:B0:45:D1:E5:DD:79:5A:7D:B5:82
            X509v3 Subject Alternative Name:
                DNS:<mdb-name>-<pod-index>.<mdb-object-name>-svc.<namespace>.svc.cluster.local, DNS:<mdb-name>-<pod-index>
    Signature Algorithm: sha256WithRSAEncryption
         ...
         ...
         ...
-----BEGIN CERTIFICATE-----
### Acutal certificate
-----END CERTIFICATE-----

```

> One of the most important things about this certificate is the
`Public Key Algorithm`. It needs to be set to `rsaEncryption` or our
database image will not be able to work with it as it uses OpenSSL
1.0.2g, and patch level versions of OpenSSL (1.0.x). The complete
investigation is in https://jira.mongodb.org/browse/CLOUDP-40599.

Each certificate will be configured with the following attributes:

* Common Name (CN): This is set to <mdb-name>-<pod-index>
* Subject: C=US, ST=NY, L=NY, O=mongodb, OU=MongoDB Kubernetes Operator, CN=<mdb-name>-<pod-index>
* Subject Alternative Names (2 entries): This list will contain two
  entries; the internal FQDN of the Pod (includes service, namespace
  and `svc.cluster.local`) and the Common Name.

# TLS Certificates In Kubernetes #

The operator will create the Certificates using Kube CA and they will
be stored in a `Secret` object with a name like
`<mdb-name>-certs`. This `Secret` will contain an entry for each `Pod`
in the deployment.


## Rotation of TLS Certificates ##

**Note: this process involves restarting the MongoDB Pods**

**Please note, use this documentation as a guide. If you are planning
on rotating the certs for a Sharded Cluster, for each Shard in the
cluster, the certs will have to be rotated (following these
principles) and finally, the same process will have to be applied to
the Mongos.**

The operator does not support the rotation of certificates, but this
can be triggered manually by removing the `Secret` object and
triggering a reconciliation, which will cause the certificates to be
reissued. After the certificates have been approved and issued,
they will be mounted in the Pods again.

### 1. Delete the Certificate Signing Request objects from Kubernetes ###

* Note, I'm assuming there's an environment variable with name
  `NAMESPACE` with the name of the namespace you are working
  on. Also, the `MDB_NAME` is the name of the `MDB` object.

These objects are useless without a Private Key (which is stored in
the Secret object). The CSR objects are not namespaced, this means
that you need to be careful about their removal. When the operator
creates a CSR it will name it as `<mdb-name>-<index>.<namespace>`. To
remove it you can use a script along the lines of:

``` bash
kubectl delete $(kubectl get csr -o name | grep "\.${NAMESPACE}\$")
```

### 2. Delete the Secret containing the Private Keys and Certs ###

The operator creates a `Secret` object that contains the Private Keys
and Certificate for each Pod, it will be named as
`<mdb-name>-cert`. This `Secret` needs to be removed like:

    kubectl -n $NAMESPACE delete "secret/${MDB_NAME}-cert"

### 3. Wait for Reconciliation ###

After next reconciliation, the Operator will realize that the `Secret`
with the certificates is not there any more and will request the
certificates again, a new set of `CSR` (`Certificate Signing Request`)
will appear in the Kubernetes cluster, and will have to be approved.

    kubectl certificate approve $(kubectl get csr -o name | grep "\.${NAMESPACE}\$")

The operator will keep watching these certificates until they are
approved and issued. When this happens, the `Secret` will be populated
again with the new Certificates and Private Keys.

Eventually, this `Secret`'s contents will be refreshed on each of the
`Pod`s containing our MongoDB databases.

### 4. Instruct the Automation Agent to Restart the MongoDBs ###

We don't currently support rotating certificates with no downtime and
this process needs to be done manually. I'm going to describe the
easiest solution to do this, which involves restarting each one of the
Pods manually.

**Warning Number 2: This will delete each Pod, one by one, waiting 60
seconds inbetween. MongoDB should be capable of maintaining the
operativity of the cluster while this process run**

``` bash
members=$(kubectl get "mdb/${MDB_NAME}" -o jsonpath='{.spec.members}')
members=$((members-1))
for i in $(seq $members -1 0); do
    kubectl -n $NAMESPACE delete "pods/${MDB_NAME}-$i"
    sleep 60
done
```

After all the `Pod`s have been restarted, they will be using the new
just issued certificates.

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
