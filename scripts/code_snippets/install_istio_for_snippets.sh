#!/usr/bin/env bash

# Replaces the istio that the kind e2e tooling pre-installs (1.16.1, pinned in
# scripts/dev/recreate_kind_clusters.sh) with a current release on the three
# member clusters, keeping the same topology: flat network multi-primary,
# shared self-signed root CA, remote secrets for cross-cluster discovery, and
# the DNS proxy (ISTIO_META_DNS_CAPTURE).
#
# Why: the docs scenarios assume a mesh close to what customers actually run,
# and 1.16 is years behind. (The reinstall was introduced chasing the mesh
# prerequisite that injected pods resolve Services existing in only one
# cluster -- om-svc in ra-06, checked by ra-04 -- but the Evergreen task
# hosts turned out unable to provide that with ANY istio version; see the
# ra-04 note in tests/test_kind_search_percluster_snippets.sh. The e2e tests
# never notice because the operator mirrors its services into every cluster.)
# The reinstall is scoped to the clusters of this task; the tooling default
# stays untouched.
#
# Modeled on multi_cluster/tools/install_istio.sh, with the istioctl syntax
# of current releases (uninstall and create-remote-secret are no longer
# under "istioctl x"). Must run from the repository root.

set -eux

export CTX_CLUSTER1=${CTX_CLUSTER1:?}
export CTX_CLUSTER2=${CTX_CLUSTER2:?}
export CTX_CLUSTER3=${CTX_CLUSTER3:?}
export VERSION=${VERSION:?}

source multi_cluster/tools/download_istio.sh

cd "istio-${VERSION}"

# remove the pre-installed istio entirely before installing the new version
pids=()
for ctx in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
  bin/istioctl uninstall --context="${ctx}" --purge --skip-confirmation &
  pids+=($!)
done
for pid in "${pids[@]}"; do wait "${pid}"; done

# shared root of trust: per-cluster intermediate CAs signed by one root
rm -rf certs
mkdir -p certs
pushd certs
make -f ../tools/certs/Makefile.selfsigned.mk "root-ca"
for ctx in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
  make -f ../tools/certs/Makefile.selfsigned.mk "${ctx}-cacerts"
  kubectl --context="${ctx}" delete ns istio-system --ignore-not-found
  kubectl --context="${ctx}" create ns istio-system
  kubectl --context="${ctx}" label --overwrite ns istio-system pod-security.kubernetes.io/enforce=privileged
  kubectl --context="${ctx}" create secret generic cacerts -n istio-system \
    --from-file="${ctx}/ca-cert.pem" \
    --from-file="${ctx}/ca-key.pem" \
    --from-file="${ctx}/root-cert.pem" \
    --from-file="${ctx}/cert-chain.pem"
done
popd

# one mesh, one network: pods reach each other by IP across the
# interconnected kind clusters, so no east-west gateways are needed
i=0
pids=()
for ctx in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
  i=$((i + 1))
  cat <<EOF >"cluster${i}.yaml"
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
        clusterName: cluster${i}
      network: network1
EOF
  bin/istioctl install --context="${ctx}" --set components.cni.enabled=true -f "cluster${i}.yaml" -y &
  pids+=($!)
done
for pid in "${pids[@]}"; do wait "${pid}"; done

# enable cross-cluster endpoint discovery: every istiod watches the other
# two clusters. The kubeconfig embeds 127.0.0.1 API URLs on kind, so point
# the remote secrets at the node-internal API endpoint instead.
api_server_url() {
  local ctx=$1
  echo "https://$(kubectl --context="${ctx}" get node "${ctx#kind-}-control-plane" -o=jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}'):6443"
}

i=0
for ctx in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
  i=$((i + 1))
  for other in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
    if [[ "${other}" == "${ctx}" ]]; then continue; fi
    bin/istioctl create-remote-secret \
      --context="${ctx}" \
      -n istio-system \
      --name="cluster${i}" \
      --server "$(api_server_url "${ctx}")" |
      kubectl apply -f - --context="${other}"
  done
done

# never inject the mesh's own namespace
for ctx in "${CTX_CLUSTER1}" "${CTX_CLUSTER2}" "${CTX_CLUSTER3}"; do
  kubectl --context="${ctx}" label --overwrite namespace istio-system istio-injection=disabled
done
