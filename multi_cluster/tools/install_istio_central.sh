#!/bin/bash

set -eux

export VERSION=1.14.2

export CTX_CLUSTER1=e2e.operator.mongokubernetes.com

curl -L https://istio.io/downloadIstio | ISTIO_VERSION=${VERSION} sh -
cd istio-${VERSION}


bin/istioctl install --context="${CTX_CLUSTER}" --set profile profile=default --set meshConfig.outboundTrafficPolicy.mode=REGISTRY_ONLY -y