# MongoDB Helper Chart
## Cert Manager Helm chart for MongoDB 

This chart provides an example for certificate generation using  cert manager for use with MongoDB Kubernetes Operator


### Prerequisite steps
Enter name of MongoDB CRD and decide on a namespace. Certificates should be deployed in the same namespace.

`` $ MONGODB_NAME=mdbreplset
``

This example uses CA Issuer, however any Cert Manager supported Issuer will work here.
Disable CA Issuer configuration by setting value `createCAIssuer: false`

Generate CA Key and CRT Files for CA Issuer. Use single root CA for all MongoDB TLS certificates.

1. `openssl genrsa -out ca.key 2048`
2. `COMMON_NAME=*.svc.cluster.local`  # _name of Kubernetes DNS root._
3. `openssl req -x509 -new -nodes -key ca.key -subj "/CN=${COMMON_NAME}" -days 3650 -reqexts v3_req -extensions v3_ca -out ca.crt`

### Deploy Helm Charts

1. Run HELM Install.

`helm install certs .`

Wait for `kubectl get certificates` to get to Ready state

2. Generated Certificates should then be converted into PEM format for MongoDB. Here are few simple commands to make secret and CA config map that could be used directly my MongODBCA

* First Member Certificate
`kubectl get secret/$MONGODB_NAME-0 -o jsonpath='{.data.tls\.crt}' | base64 --decode > $MONGODB_NAME-0-pem`
`kubectl get secret/$MONGODB_NAME-0 -o jsonpath='{.data.tls\.key}' | base64 --decode >> $MONGODB_NAME-0-pem`
* Second Member Certificate
`kubectl get secret/$MONGODB_NAME-1 -o jsonpath='{.data.tls\.crt}' | base64 --decode > $MONGODB_NAME-1-pem`
`kubectl get secret/$MONGODB_NAME-1 -o jsonpath='{.data.tls\.key}' | base64 --decode >> $MONGODB_NAME-1-pem`

* Third Member Certificate
`kubectl get secret/$MONGODB_NAME-2 -o jsonpath='{.data.tls\.crt}' | base64 --decode > $MONGODB_NAME-2-pem`
`kubectl get secret/$MONGODB_NAME-2 -o jsonpath='{.data.tls\.key}' | base64 --decode >> $MONGODB_NAME-2-pem`

..Repeat if MongoDB ReplicaSet will have more then 3 nodes

#### Create Secret for MongoDB CR with PEM files 
```kubectl delete secret $MONGODB_NAME-cert```
```kubectl create secret generic $MONGODB_NAME-cert --from-file=$MONGODB_NAME-0-pem --from-file=$MONGODB_NAME-1-pem --from-file=$MONGODB_NAME-2-pem```

#### (optional) Remove temp files with PEM certificates

rm $MONGODB_NAME-1-*


#### Generate CA config map

`cat ca.crt > ca-pem`
`kubectl create configmap $MONGODB_NAME-ca --from-file=ca-pem`






