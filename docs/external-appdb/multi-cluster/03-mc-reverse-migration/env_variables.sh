# ======================================================================
# Multi-cluster External AppDB — Reverse Migration: customer configuration
#
# Edit the <placeholder> values, then:  source env_variables.sh
#
# PREREQUISITE: this walkthrough uses a MongoDBMultiCluster as the external
# AppDB (spec.externalApplicationDatabaseRef.kind: MongoDBMultiCluster). That
# capability requires an operator build that supports it (CLOUDP-444251 / the
# multi-cluster external-AppDB feature). It does not work on older operators.
# ======================================================================

# ----------------------------------------------------------------------
# KUBERNETES (3 clusters)
#   K8S_CTX_0 = central cluster: operator, cert-manager, both Ops Managers
#               and the MongoDBMultiCluster CR object all live here.
#   K8S_CTX_1 / K8S_CTX_2 = member clusters that host the AppDB data pods.
# Run: kubectl config get-contexts
# ----------------------------------------------------------------------
export K8S_CTX_0="<central cluster context>"
export K8S_CTX_1="<appdb member cluster 1 context>"
export K8S_CTX_2="<appdb member cluster 2 context>"

# Namespace used in every cluster for the operator, Ops Managers and the AppDB
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
#   management OM  ->  manages the MongoDBMultiCluster(role: AppDB) CR
#   primary OM     ->  uses that CR as its AppDB (externalApplicationDatabaseRef)
#
# The AppDB CR name MUST equal "<primary-om-name>-db" (operator validation).
# AppDB members are spread across the two member clusters.
# ----------------------------------------------------------------------
export MANAGEMENT_OM_NAME="management-om"
export PRIMARY_OM_NAME="primary-om"

# AppDB members per member cluster (clusterSpecList).
export APPDB_MEMBERS_C1=2
export APPDB_MEMBERS_C2=2

# Ops Manager admin user (used to create the first global admin of each OM)
export OM_ADMIN_USER="admin"
export OM_ADMIN_PASSWORD="Passw0rd."
export OM_ADMIN_FIRST_NAME="Admin"
export OM_ADMIN_LAST_NAME="User"
export OM_ADMIN_EMAIL="admin@example.com"

# Ops Manager project the AppDB CR is registered under, on the management OM
export APPDB_PROJECT_NAME="external-appdb"

# ----------------------------------------------------------------------
# TLS (multi-cluster AppDB members are TLS-enabled)
# ----------------------------------------------------------------------
export CERT_MANAGER_NAMESPACE="cert-manager"
# cert-manager issuers / CA material used to sign the AppDB server certificate.
export APPDB_TLS_SELF_SIGNED_ISSUER="mongodb-self-signed-issuer"
export APPDB_TLS_CA_CERT_NAME="mongodb-ca"
export APPDB_TLS_CA_SECRET_NAME="mongodb-ca-key-pair"
export APPDB_TLS_CA_ISSUER="mongodb-ca-issuer"
# Secret-name prefix for the AppDB server certificate (operator convention:
# the cert secret is "<prefix>-<appdb-name>-cert").
export APPDB_CERT_PREFIX="appdb"

# ----------------------------------------------------------------------
# DERIVED VALUES (do not edit)
# ----------------------------------------------------------------------
# AppDB CR name is fixed by the operator's naming convention.
export APPDB_NAME="${PRIMARY_OM_NAME}-db"
# In-cluster URL of the management Ops Manager (on the central cluster).
export MANAGEMENT_OM_URL="http://${MANAGEMENT_OM_NAME}-svc.${MDB_NS}.svc.cluster.local:8080"
# Programmatic API-key Secret the operator provisions for the management OM
# (new naming format: "<namespace>-<om-name>-admin-key").
export MANAGEMENT_OM_ADMIN_KEY_SECRET="${MDB_NS}-${MANAGEMENT_OM_NAME}-admin-key"
# Project connection ConfigMap the AppDB CR points at.
export APPDB_PROJECT_CONFIGMAP="${APPDB_NAME}-config"
# Secret holding the AppDB connection string the operator computes for the primary OM.
export APPDB_CONNECTION_STRING_SECRET="${APPDB_NAME}-connection-string"
# CA ConfigMap (ca-pem + mms-ca.crt) the AppDB and OM trust for TLS.
export APPDB_CA_CONFIGMAP="${APPDB_NAME}-ca"
