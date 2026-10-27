kind delete cluster --name kind
kind create cluster --name kind

# cd docs/external-appdb/01-fresh-start

source ./env_variables_e2e_public.sh
#   → K8S_CTX=kind-kind, OM_VERSION=8.0.7, APPDB_VERSION=8.0.5-ent,
#     OPERATOR_HELM_CHART=oci://quay.io/mongodb/helm-charts/mongodb-kubernetes

bash ./test.sh
