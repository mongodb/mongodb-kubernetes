AWS_REGION = "us-east-1"

KUBECONFIG_FILEPATH = "/etc/config/kubeconfig"
MULTI_CLUSTER_CONFIG_DIR = "/etc/multicluster"
# AppDB monitoring is disabled by default for e2e tests.
# If monitoring is needed use monitored_appdb_operator_installation_config / operator_with_monitored_appdb
MONITOR_APPDB_E2E_DEFAULT = "true"
CLUSTER_HOST_MAPPING = {
    "us-central1-c_central": "https://35.232.85.244",
    "us-east1-b_member-1a": "https://35.243.222.230",
    "us-east1-c_member-2a": "https://34.75.94.207",
    "us-west1-a_member-3a": "https://35.230.121.15",
}

LEGACY_CENTRAL_CLUSTER_NAME: str = "__default"
LEGACY_DEPLOYMENT_STATE_VERSION: str = "1.27.0"

# Helm charts
LEGACY_OPERATOR_CHART = "mongodb/enterprise-operator"
MCK_HELM_CHART = "mongodb/mongodb-kubernetes"
LOCAL_HELM_CHART_DIR = "helm_chart"

OFFICIAL_OPERATOR_IMAGE_NAME = "mongodb-kubernetes"
LEGACY_OPERATOR_IMAGE_NAME = "mongodb-enterprise-operator-ubi"

# Names for operator and RBAC
OPERATOR_NAME = "mongodb-kubernetes-operator"
MULTI_CLUSTER_OPERATOR_NAME = OPERATOR_NAME + "-multi-cluster"
LEGACY_OPERATOR_NAME = "mongodb-enterprise-operator"
LEGACY_MULTI_CLUSTER_OPERATOR_NAME = LEGACY_OPERATOR_NAME + "-multi-cluster"
APPDB_SA_NAME = "mongodb-kubernetes-appdb"
DATABASE_SA_NAME = "mongodb-kubernetes-database-pods"
OM_SA_NAME = "mongodb-kubernetes-ops-manager"
TELEMETRY_CONFIGMAP_NAME = LEGACY_OPERATOR_NAME + "-telemetry"
MULTI_CLUSTER_MEMBER_LIST_CONFIGMAP = OPERATOR_NAME + "-member-list"
