#!/bin/bash

set -eux

export CTX_CLUSTER1=e2e.cluster1.mongokubernetes.com
export CTX_CLUSTER2=e2e.cluster2.mongokubernetes.com
export VERSION=1.9.1

# download Istio 1.9.1 under the path 
curl -L https://istio.io/downloadIstio | ISTIO_VERSION=${VERSION} sh -

cd istio-${VERSION}
mkdir -p certs
pushd certs

# create root trust for the clusters
make -f ../tools/certs/Makefile.selfsigned.mk root-ca
make -f ../tools/certs/Makefile.selfsigned.mk ${CTX_CLUSTER1}-cacerts
make -f ../tools/certs/Makefile.selfsigned.mk ${CTX_CLUSTER2}-cacerts

# create cluster secret objects with the certs and keys
kubectl --context="${CTX_CLUSTER1}" create ns istio-system
kubectl --context="${CTX_CLUSTER1}" create secret generic cacerts -n istio-system \
      --from-file=${CTX_CLUSTER1}/ca-cert.pem \
      --from-file=${CTX_CLUSTER1}/ca-key.pem \
      --from-file=${CTX_CLUSTER1}/root-cert.pem \
      --from-file=${CTX_CLUSTER1}/cert-chain.pem

kubectl --context="${CTX_CLUSTER2}" create ns istio-system
kubectl --context="${CTX_CLUSTER2}" create secret generic cacerts -n istio-system \
      --from-file=${CTX_CLUSTER2}/ca-cert.pem \
      --from-file=${CTX_CLUSTER2}/ca-key.pem \
      --from-file=${CTX_CLUSTER2}/root-cert.pem \
      --from-file=${CTX_CLUSTER2}/cert-chain.pem
popd

# install IstioOperator in clusters
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

istioctl install --context="${CTX_CLUSTER1}" -f cluster1.yaml -y

cat <<EOF > cluster2.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster2
      network: network1
EOF

istioctl install --context="${CTX_CLUSTER2}" -f cluster2.yaml -y


# enable endpoint discovery
istioctl x create-remote-secret \
    --context="${CTX_CLUSTER1}" \
    --name=cluster1 | \
    kubectl apply -f - --context="${CTX_CLUSTER2}"


istioctl x create-remote-secret \
    --context="${CTX_CLUSTER2}" \
    --name=cluster2 | \
    kubectl apply -f - --context="${CTX_CLUSTER1}"

# disable namespace injection explicitly for istio-system namespace
kubectl --context="${CTX_CLUSTER1}" label namespace istio-system istio-injection=disabled
kubectl --context="${CTX_CLUSTER2}" label namespace istio-system istio-injection=disabled

# cleanup: delete the istio repo at the end
cd ..
rm -r istio-${VERSION}
rm -f cluster1.yaml cluster2.yaml
