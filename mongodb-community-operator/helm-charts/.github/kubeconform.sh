#!/bin/bash
set -uxo pipefail

# forked
git remote add mongo https://github.com/mongodb/helm-charts.git
git fetch mongo

CHART_DIRS="$(git diff --find-renames --name-only "$(git rev-parse --abbrev-ref HEAD)" mongo/add-role -- charts | grep -i chart.yaml | xargs -r dirname)"
KUBECONFORM_VERSION="v0.5.0"
SCHEMA_LOCATION="https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/"

# install kubeconform
curl --silent --show-error --fail --location --output /tmp/kubeconform.tar.gz "https://github.com/yannh/kubeconform/releases/download/${KUBECONFORM_VERSION}/kubeconform-linux-amd64.tar.gz"
tar -xf /tmp/kubeconform.tar.gz kubeconform

# validate charts
helm repo add mongodb https://mongodb.github.io/helm-charts
for CHART_DIR in ${CHART_DIRS}; do
  if [[ ! -d "$CHART_DIR" ]]; then
    continue
  fi
  helm dependency update "${CHART_DIR}"
  helm template "${CHART_DIR}" | ./kubeconform --strict --ignore-missing-schemas --kubernetes-version "${KUBERNETES_VERSION#v}" --schema-location "${SCHEMA_LOCATION}"
done
