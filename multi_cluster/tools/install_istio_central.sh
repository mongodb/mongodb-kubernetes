#!/usr/bin/env bash

set -eux

export VERSION=${VERSION:-1.31.3}

export CTX_CLUSTER=${CTX_CLUSTER:-e2e.operator.mongokubernetes.com}

source multi_cluster/tools/download_istio.sh
cd "istio-${VERSION}"

bin/istioctl uninstall --context="${CTX_CLUSTER}" --purge --skip-confirmation
bin/istioctl install --context="${CTX_CLUSTER}" --set components.cni.enabled=true --set profile=default --set meshConfig.outboundTrafficPolicy.mode=REGISTRY_ONLY --set values.pilot.env.ENABLE_NATIVE_SIDECARS=false --skip-confirmation
