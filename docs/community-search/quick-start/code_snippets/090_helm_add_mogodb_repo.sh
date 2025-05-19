helm repo add mongodb https://mongodb.github.io/helm-charts
helm repo update mongodb
if [[ "${OPERATOR_HELM_CHART}" != "helm_chart" ]]; then helm search repo "${OPERATOR_HELM_CHART}"; fi;
