# Enable X.509 Authentication

You can use Helm or `kubectl` to enable X.509 authentication for the
MongoDB Agent and client.

## Prerequisites

1. Add the `cert-manager` repository to your `helm` repository list and
   ensure it's up to date:

   ```
   helm repo add jetstack https://charts.jetstack.io
   helm repo update
   ```

1. Install `cert-manager`:

   ```
   helm install cert-manager jetstack/cert-manager --namespace cert-manager \
   --create-namespace --set installCRDs=true
   ```

## Use Helm to Enable X.509 Authentication

You can use Helm to install the MongoDB Community Kubernetes Operator
and then create the required `cert-manager` resources and a
`MongoDBCommunity` custom resource with X.509 authentication.

To learn more about installing the operator, see [Install the Operator using Helm](https://github.com/mongodb/mongodb-kubernetes/blob/master/docs/install-upgrade.md#install-the-operator-using-helm).

1. Install the operator:

   ```
   helm upgrade --install community-operator mongodb/community-operator \
   --namespace <namespace> --create-namespace
   ```

2. Create `cert-manager` resources for TLS and X.509 authentication.
   Save the following YAML to a file (e.g. `x509-certs.yaml`), replacing
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
   # CA issuer that signs certificates
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
     duration: 8760h
     renewBefore: 720h
     commonName: "*.<resource-name>-svc.<namespace>.svc.cluster.local"
     dnsNames:
       - "*.<resource-name>-svc.<namespace>.svc.cluster.local"
   ---
   # Agent X.509 certificate
   apiVersion: cert-manager.io/v1
   kind: Certificate
   metadata:
     name: agent-certs
     namespace: <namespace>
   spec:
     commonName: mms-automation-agent
     dnsNames:
       - automation
     duration: 240h
     issuerRef:
       name: tls-ca-issuer
     renewBefore: 120h
     secretName: agent-certs
     subject:
       countries:
         - US
       localities:
         - NY
       organizationalUnits:
         - a-1635241837-m5yb81lfnrz
       organizations:
         - cluster.local-agent
       provinces:
         - NY
     usages:
       - digital signature
       - key encipherment
       - client auth
   ---
   # Sample X.509 client certificate
   apiVersion: cert-manager.io/v1
   kind: Certificate
   metadata:
     name: x509-user-cert
     namespace: <namespace>
   spec:
     commonName: my-x509-user
     duration: 240h
     issuerRef:
       name: tls-ca-issuer
     renewBefore: 120h
     secretName: my-x509-user-cert
     subject:
       organizationalUnits:
         - organizationalunit
       organizations:
         - organization
     usages:
       - digital signature
       - client auth
   ```

   Apply the file:

   ```
   kubectl apply -f x509-certs.yaml --namespace <namespace>
   ```

3. Create a `MongoDBCommunity` resource with X.509 authentication. For
   an example, see [mongodb.com_v1_mongodbcommunity_x509.yaml](https://github.com/mongodb/mongodb-kubernetes/blob/master/public/samples/community/mongodb.com_v1_mongodbcommunity_x509.yaml).

   Apply the file:

   ```
   kubectl apply -f <your-mongodb-resource>.yaml --namespace <namespace>
   ```

## Use `kubectl` to Enable X.509 Authentication

You can use Helm to install and deploy the MongoDB Community Kubernetes
Operator with X.509 Authentication enabled for the MongoDB Agent and
client.

1. To install the MongoDB Community Kubernetes Operator, see
   [Install the Operator using kubectl](https://github.com/mongodb/mongodb-kubernetes/blob/master/docs/install-upgrade.md#install-the-operator-using-kubectl).

2. To create a CA, ConfigMap, secrets, issuer, and certificate, see
   [Enable External Access to a MongoDB Deployment](https://github.com/mongodb/mongodb-kubernetes/blob/master/docs/external_access.md).

3. Create a YAML file for the  MongoDB Agent certificate. For an example,
   see [agent-certificate.yaml](https://github.com/mongodb/mongodb-kubernetes/blob/master/public/samples/community/external_access/agent-certificate.yaml).

   **Note:**

   - For the `spec.issuerRef.name` parameter, specify the
     `cert-manager` issuer that you created previously.
   - For the `spec.secretName` parameter, specify the same
     value as the `spec.security.authentication.agentCertificateSecretRef`
     parameter in your resource. This secret should contain a signed
     X.509 certificate and a private key for the MongoDB agent.

4. To apply the file, copy and paste the following command and replace
   the `<agent-certificate>` variable with the name of your MongoDB Agent
   certificate and the `<namespace>` variable with the namespace:

   ```
   kubectl apply -f <agent-certificate>.yaml --namespace <namespace>
   ```

5. Create a YAML file for your resource. For an example, see
   [mongodb.com_v1_mongodbcommunity_x509.yaml](https://github.com/mongodb/mongodb-kubernetes/blob/master/public/samples/community/mongodb.com_v1_mongodbcommunity_x509.yaml).

   **Note:**

   - For the `spec.security.tls.certificateKeySecretRef.name` parameter,
     specify a reference to the secret that contains the private key and
     certificate to use for TLS. The operator expects the PEM encoded key
     and certificate available at "tls.key" and "tls.crt". Use the same
     format used for the standard "kubernetes.io/tls" Secret type, but no
     specific type is required. Alternatively, you can provide
     an entry called "tls.pem" that contains the concatenation of the
     certificate and key. If all of "tls.pem", "tls.crt" and "tls.key"
     are present, the "tls.pem" entry needs to equal the concatenation
     of "tls.crt" and "tls.key".

   - For the `spec.security.tls.caConfigMapRef.name` parameter, specify
     the ConfigMap that you created previously.

   - For the `spec.authentication.modes` parameter, specify `X509`.

   - If you have multiple authentication modes, specify the
     `spec.authentication.agentMode` parameter.

   - The `spec.authentication.agentCertificateSecretRef` parameter
     defaults to `agent-certs`.

   - For the `spec.users.db` parameter, specify `$external`.

   - Do not set the `spec.users.scramCredentialsSecretName` parameter
     and the `spec.users.passwordSecretRef` parameters.

6. To apply the file, copy and paste the following command and replace
   the `<replica-set>` variable with your resource and the `<namespace>`
   variable with the namespace:

   ```
   kubectl apply -f <replica-set>.yaml --namespace <namespace>
   ```

7. Create a YAML file for the client certificate. For an example, see
   [cert-x509.yaml](https://github.com/mongodb/mongodb-kubernetes/blob/master/public/samples/community/external_access/cert-x509.yaml).

8. To apply the file, copy and paste the following command and replace
   the `<client-certificate>` variable with the name of your client
   certificate and the `<namespace>` variable with the namespace:

   ```
   kubectl apply -f <client-certificate>.yaml --namespace <namespace>
   ```
