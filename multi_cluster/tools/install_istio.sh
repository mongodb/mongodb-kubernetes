#!/usr/bin/env bash

set -eux

export CTX_CLUSTER1=${CTX_CLUSTER1:-e2e.cluster1.mongokubernetes.com}
export CTX_CLUSTER2=${CTX_CLUSTER2:-e2e.cluster2.mongokubernetes.com}
export CTX_CLUSTER3=${CTX_CLUSTER3:-e2e.cluster3.mongokubernetes.com}
export VERSION=${VERSION:-1.12.8}

IS_KIND="false"
if [[ ${CTX_CLUSTER1} = kind* ]]; then
  IS_KIND="true"
fi

source multi_cluster/tools/download_istio.sh

#
cd "istio-${VERSION}"
## perform cleanup prior to install
bin/istioctl x uninstall --context="${CTX_CLUSTER1}" --purge --skip-confirmation &
bin/istioctl x uninstall --context="${CTX_CLUSTER2}" --purge --skip-confirmation &
bin/istioctl x uninstall --context="${CTX_CLUSTER3}" --purge --skip-confirmation &
wait

rm -rf certs
mkdir -p certs
pushd certs

# create root trust for the clusters
make -f ../tools/certs/Makefile.selfsigned.mk "root-ca"
# FIXME: I'm not sure why, but Istio's makefiles seem to fail on my Mac when generating those certs.
# The funny thing is that they are generated fine once I rerun the targets.
# This probably requires a bit more investigation or upgrading Istio to the latest version.
make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER1}-cacerts" || make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER1}-cacerts"
make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER2}-cacerts" || make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER2}-cacerts"
make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER3}-cacerts" || make -f ../tools/certs/Makefile.selfsigned.mk "${CTX_CLUSTER3}-cacerts"

# create cluster secret objects with the certs and keys
kubectl --context="${CTX_CLUSTER1}" delete ns istio-system || true
kubectl --context="${CTX_CLUSTER1}" create ns istio-system
kubectl --context="${CTX_CLUSTER1}" label --overwrite ns istio-system pod-security.kubernetes.io/enforce=privileged
kubectl --context="${CTX_CLUSTER1}" create secret generic cacerts -n istio-system \
  --from-file="${CTX_CLUSTER1}/ca-cert.pem" \
  --from-file="${CTX_CLUSTER1}/ca-key.pem" \
  --from-file="${CTX_CLUSTER1}/root-cert.pem" \
  --from-file="${CTX_CLUSTER1}/cert-chain.pem"

kubectl --context="${CTX_CLUSTER2}" delete ns istio-system || true
kubectl --context="${CTX_CLUSTER2}" create ns istio-system
kubectl --context="${CTX_CLUSTER2}" label --overwrite ns istio-system pod-security.kubernetes.io/enforce=privileged
kubectl --context="${CTX_CLUSTER2}" create secret generic cacerts -n istio-system \
  --from-file="${CTX_CLUSTER2}/ca-cert.pem" \
  --from-file="${CTX_CLUSTER2}/ca-key.pem" \
  --from-file="${CTX_CLUSTER2}/root-cert.pem" \
  --from-file="${CTX_CLUSTER2}/cert-chain.pem"

kubectl --context="${CTX_CLUSTER3}" delete ns istio-system || true
kubectl --context="${CTX_CLUSTER3}" create ns istio-system
kubectl --context="${CTX_CLUSTER3}" label --overwrite ns istio-system pod-security.kubernetes.io/enforce=privileged
kubectl --context="${CTX_CLUSTER3}" create secret generic cacerts -n istio-system \
  --from-file="${CTX_CLUSTER3}/ca-cert.pem" \
  --from-file="${CTX_CLUSTER3}/ca-key.pem" \
  --from-file="${CTX_CLUSTER3}/root-cert.pem" \
  --from-file="${CTX_CLUSTER3}/cert-chain.pem"
popd

# install IstioOperator in clusters
cat <<EOF >cluster1.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  tag: ${VERSION}
  components:
    cni:
      namespace: istio-system
      enabled: true
  meshConfig:
    defaultConfig:
      terminationDrainDuration: 30s
      proxyMetadata:
        ISTIO_META_DNS_AUTO_ALLOCATE: "true"
        ISTIO_META_DNS_CAPTURE: "true"
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster1
      network: network1
EOF

bin/istioctl install --context="${CTX_CLUSTER1}" --set components.cni.enabled=true -f cluster1.yaml -y &

cat <<EOF >cluster2.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  tag: ${VERSION}
  components:
    cni:
      namespace: istio-system
      enabled: true
  meshConfig:
    defaultConfig:
      terminationDrainDuration: 30s
      proxyMetadata:
        ISTIO_META_DNS_AUTO_ALLOCATE: "true"
        ISTIO_META_DNS_CAPTURE: "true"
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster2
      network: network1
EOF

bin/istioctl install --context="${CTX_CLUSTER2}" --set components.cni.enabled=true -f cluster2.yaml -y &

cat <<EOF >cluster3.yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  tag: ${VERSION}
  components:
    cni:
      namespace: istio-system
      enabled: true
  meshConfig:
    defaultConfig:
      terminationDrainDuration: 30s
      proxyMetadata:
        ISTIO_META_DNS_AUTO_ALLOCATE: "true"
        ISTIO_META_DNS_CAPTURE: "true"
  values:
    global:
      meshID: mesh1
      multiCluster:
        clusterName: cluster3
      network: network1
EOF

bin/istioctl install --context="${CTX_CLUSTER3}" --set components.cni.enabled=true -f cluster3.yaml -y &

wait

CLUSTER_1_ADDITIONAL_OPTS=""
CLUSTER_2_ADDITIONAL_OPTS=""
CLUSTER_3_ADDITIONAL_OPTS=""
if [[ ${IS_KIND} == "true" ]]; then
  CLUSTER_1_ADDITIONAL_OPTS="--server https://$(kubectl --context="${CTX_CLUSTER1}" get node e2e-cluster-1-control-plane -o=jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'):6443"
  CLUSTER_2_ADDITIONAL_OPTS="--server https://$(kubectl --context="${CTX_CLUSTER2}" get node e2e-cluster-2-control-plane -o=jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'):6443"
  CLUSTER_3_ADDITIONAL_OPTS="--server https://$(kubectl --context="${CTX_CLUSTER3}" get node e2e-cluster-3-control-plane -o=jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'):6443"
fi

# enable endpoint discovery
# shellcheck disable=SC2086 # CLUSTER_X_ADDITIONAL_OPTS must not be quoted - empty string breaks istioctl
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER1}" \
  -n istio-system \
  --name=cluster1 ${CLUSTER_1_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER2}"

# shellcheck disable=SC2086
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER1}" \
  -n istio-system \
  --name=cluster1 ${CLUSTER_1_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER3}"

# shellcheck disable=SC2086
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER2}" \
  -n istio-system \
  --name=cluster2 ${CLUSTER_2_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER1}"

# shellcheck disable=SC2086
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER2}" \
  -n istio-system \
  --name=cluster2 ${CLUSTER_2_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER3}"

# shellcheck disable=SC2086
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER3}" \
  -n istio-system \
  --name=cluster3 ${CLUSTER_3_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER1}"

# shellcheck disable=SC2086
bin/istioctl x create-remote-secret \
  --context="${CTX_CLUSTER3}" \
  -n istio-system \
  --name=cluster3 ${CLUSTER_3_ADDITIONAL_OPTS} |
  kubectl apply -f - --context="${CTX_CLUSTER2}"
# disable namespace injection explicitly for istio-system namespace
kubectl --context="${CTX_CLUSTER1}" label namespace istio-system istio-injection=disabled
kubectl --context="${CTX_CLUSTER2}" label namespace istio-system istio-injection=disabled
kubectl --context="${CTX_CLUSTER3}" label namespace istio-system istio-injection=disabled

# Skipping the cleanup for now. Otherwise we won't have the tools to diagnose issues
#cd ..
#rm -r istio-${VERSION}
#rm -f cluster1.yaml cluster2.yaml cluster3.yaml
