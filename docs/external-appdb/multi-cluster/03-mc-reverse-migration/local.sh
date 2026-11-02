# Local runner for the multi-cluster external-AppDB fresh-start walkthrough.
#
# Unlike the single-cluster suites, this needs a multi-cluster kind environment
# (a central cluster + member clusters). Recreate it from the repo root:
#
#   DELETE_KIND_NETWORK=true scripts/dev/recreate_kind_clusters.sh
#
# That provisions the kind-e2e-cluster-1/2/3 contexts used below.
#
# Then, from this directory:
#   cd docs/external-appdb/multi-cluster/01-mc-fresh-start

source ./env_variables_e2e_public.sh
#   → K8S_CTX_0=kind-e2e-cluster-1, K8S_CTX_1=kind-e2e-cluster-2,
#     K8S_CTX_2=kind-e2e-cluster-3, OM_VERSION=8.0.7, APPDB_VERSION=8.0.5-ent

bash ./test.sh
