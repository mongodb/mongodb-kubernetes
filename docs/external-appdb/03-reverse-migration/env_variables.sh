# ======================================================================
# External AppDB — Fresh Start: customer configuration
#
# Edit the <placeholder> values, then:  source env_variables.sh
# ======================================================================

# ----------------------------------------------------------------------
# KUBERNETES
# ----------------------------------------------------------------------
# Your Kubernetes context (run: kubectl config get-contexts)
export K8S_CTX="<local cluster context>"

# Namespace for the operator, the management Ops Manager and the AppDB
export MDB_NS="mongodb"

# ----------------------------------------------------------------------
# OPERATOR
# ----------------------------------------------------------------------
HELM_REPO="oci://quay.io/mongodb/helm-charts"
export OPERATOR_HELM_CHART="${HELM_REPO}/mongodb-kubernetes"
export OPERATOR_ADDITIONAL_HELM_VALUES=""

# ----------------------------------------------------------------------
# VERSIONS
# ----------------------------------------------------------------------
export OM_VERSION="8.0.7"          # Ops Manager version (management + primary)
export APPDB_VERSION="8.0.5-ent"   # MongoDB version for the AppDB

# ----------------------------------------------------------------------
# TOPOLOGY
#   management OM  ->  manages the MongoDB(role: AppDB) CR
#   primary OM     ->  uses that CR as its AppDB (externalApplicationDatabaseRef)
#
# The AppDB CR name MUST equal "<primary-om-name>-db" (operator validation).
# ----------------------------------------------------------------------
export MANAGEMENT_OM_NAME="management-om"
export PRIMARY_OM_NAME="primary-om"

# Ops Manager admin user (used to create the first global admin of each OM)
export OM_ADMIN_USER="admin"
export OM_ADMIN_PASSWORD="Passw0rd."
export OM_ADMIN_FIRST_NAME="Admin"
export OM_ADMIN_LAST_NAME="User"
export OM_ADMIN_EMAIL="admin@example.com"

# Ops Manager project the AppDB CR is registered under, on the management OM
export APPDB_PROJECT_NAME="external-appdb"

# ----------------------------------------------------------------------
# DERIVED VALUES (do not edit)
# ----------------------------------------------------------------------
# AppDB CR name is fixed by the operator's naming convention.
export APPDB_NAME="${PRIMARY_OM_NAME}-db"
# In-cluster URL of the management Ops Manager.
export MANAGEMENT_OM_URL="http://${MANAGEMENT_OM_NAME}-svc.${MDB_NS}.svc.cluster.local:8080"
# Programmatic API-key Secret the operator provisions for the management OM
# (new naming format: "<namespace>-<om-name>-admin-key").
export MANAGEMENT_OM_ADMIN_KEY_SECRET="${MDB_NS}-${MANAGEMENT_OM_NAME}-admin-key"
# Project connection ConfigMap the AppDB CR points at.
export APPDB_PROJECT_CONFIGMAP="${APPDB_NAME}-config"
# Secret holding the AppDB connection string the operator computes for the primary OM.
export APPDB_CONNECTION_STRING_SECRET="${APPDB_NAME}-connection-string"
