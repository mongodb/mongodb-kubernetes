#!/bin/bash

set -eux

export VERSION=1.14.2

export CTX_CLUSTER=e2e.operator.mongokubernetes.com

curl -L https://istio.io/downloadIstio | ISTIO_VERSION=${VERSION} sh -
cd istio-${VERSION}

bin/istioctl x uninstall --context="${CTX_CLUSTER}" --purge
bin/istioctl install --context="${CTX_CLUSTER}" --set profile=default --set meshConfig.outboundTrafficPolicy.mode=REGISTRY_ONLY -y
