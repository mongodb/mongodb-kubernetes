#!/bin/bash

set -eux

export VERSION=${VERSION:-1.14.2}

export CTX_CLUSTER=${CTX_CLUSTER:-e2e.operator.mongokubernetes.com}

source multi_cluster/tools/download_istio.sh
cd istio-${VERSION}

bin/istioctl x uninstall --context="${CTX_CLUSTER}" --purge --skip-confirmation
bin/istioctl install --context="${CTX_CLUSTER}" --set profile=default --set meshConfig.outboundTrafficPolicy.mode=REGISTRY_ONLY --skip-confirmation
