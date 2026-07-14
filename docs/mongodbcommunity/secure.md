# Secure MongoDBCommunity Resources #

## Table of Contents

- [Secure MongoDBCommunity Resource Connections using TLS](#secure-mongodbcommunity-resource-connections-using-tls)
  - [Prerequisites](#prerequisites)
  - [Procedure](#procedure)

## Secure MongoDBCommunity Resource Connections using TLS

You can configure the MongoDB Community Kubernetes Operator to use TLS 
certificates to encrypt traffic between:

- MongoDB hosts in a replica set, and
- Client applications and MongoDB deployments.

The Operator automates TLS configuration through its integration with 
[cert-manager](https://cert-manager.io/), a certificate management tool for 
Kubernetes.

### Prerequisites

Before you secure MongoDBCommunity resource connections using TLS, you 
must [Create a database user](../docs/users.md) to authenticate to your 
MongoDBCommunity resource.

### Procedure

To secure connections to MongoDBCommunity resources with TLS using `cert-manager`:

1. Add the `cert-manager` repository to your `helm` repository list and
   ensure it's up to date:

   ```
   helm repo add jetstack https://charts.jetstack.io
   helm repo update
   ```

2. Install `cert-manager`:

   ```
   helm install cert-manager jetstack/cert-manager --namespace cert-manager --create-namespace --set crds.enabled=true
   ```

3. Create `cert-manager` resources to generate TLS certificates for your
   MongoDBCommunity resource. This assumes you already have the operator
   installed in namespace `<namespace>`.

   Save the following YAML to a file (e.g. `tls-certs.yaml`), replacing
   `<resource-name>` with the name of your MongoDBCommunity resource and
   `<namespace>` with your namespace:

   ```yaml
   # Self-signed issuer to bootstrap the CA
   apiVersion: cert-manager.io/v1
   kind: Issuer
   metadata:
     name: tls-selfsigned-issuer
     namespace: <namespace>
   spec:
     selfSigned: {}
   ---
   # Self-signed CA certificate
   apiVersion: cert-manager.io/v1
   kind: Certificate
   metadata:
     name: tls-selfsigned-ca
     namespace: <namespace>
   spec:
     isCA: true
     commonName: "*.<resource-name>-svc.<namespace>.svc.cluster.local"
     dnsNames:
       - "*.<resource-name>-svc.<namespace>.svc.cluster.local"
     secretName: tls-ca-key-pair
     privateKey:
       algorithm: ECDSA
       size: 256
     issuerRef:
       name: tls-selfsigned-issuer
       kind: Issuer
   ---
   # CA issuer that signs server certificates
   apiVersion: cert-manager.io/v1
   kind: Issuer
   metadata:
     name: tls-ca-issuer
     namespace: <namespace>
   spec:
     ca:
       secretName: tls-ca-key-pair
   ---
   # TLS certificate for the MongoDB replica set
   apiVersion: cert-manager.io/v1
   kind: Certificate
   metadata:
     name: cert-manager-tls-certificate
     namespace: <namespace>
   spec:
     secretName: tls-certificate
     issuerRef:
       name: tls-ca-issuer
       kind: Issuer
     duration: 8760h    # 365 days
     renewBefore: 720h  # 30 days
     commonName: "*.<resource-name>-svc.<namespace>.svc.cluster.local"
     dnsNames:
       - "*.<resource-name>-svc.<namespace>.svc.cluster.local"
   ```

   Apply the file:

   ```
   kubectl apply -f tls-certs.yaml --namespace <namespace>
   ```

   **Note:** `cert-manager` automatically reissues certificates before
   they expire. To change the reissuance interval, update `spec.renewBefore`
   on the Certificate resource.

4. Create a `MongoDBCommunity` resource with TLS enabled. For an example,
   see [mongodb.com_v1_mongodbcommunity_tls_cr.yaml](https://github.com/mongodb/mongodb-kubernetes/blob/master/public/samples/community/mongodb.com_v1_mongodbcommunity_tls_cr.yaml).

   Ensure the `spec.security.tls.certificateKeySecretRef.name` and
   `spec.security.tls.caConfigMapRef.name` match the secret and
   ConfigMap created by `cert-manager` in the previous step.

   Apply the file:

   ```
   kubectl apply -f <your-mongodb-resource>.yaml --namespace <namespace>
   ```

5. Test your connection over TLS by 

   - Connecting to a `mongod` container inside a pod using `kubectl`:

   ```
   kubectl exec -it <mongodb-replica-set-pod> -c mongod -- bash
   ```

   Where `mongodb-replica-set-pod` is the name of a pod from your MongoDBCommunity resource

   - Then, use `mongosh` to connect over TLS:
   For how to get the connection string look at [Deploy A Replica Set](deploy-configure.md#deploy-a-replica-set)

   ```
   mongosh "<connection-string>" --tls --tlsCAFile /var/lib/tls/ca/ca.crt --tlsCertificateKeyFile /var/lib/tls/server/*.pem 
   ```

   Where `mongodb-replica-set` is the name of your MongoDBCommunity 
   resource, `namespace` is the namespace of your deployment
   and  `connection-string` is a connection string for your `<mongodb-replica-set>-svc` service.