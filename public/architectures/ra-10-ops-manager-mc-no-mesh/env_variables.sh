# This script builds on top of the environment configured in the setup guides.
# It depends (uses) the following env variables defined there to work correctly.
# If you don't use the setup guide to bootstrap the environment, then define them here.
#  ${K8S_CLUSTER_0_CONTEXT_NAME}
#  ${K8S_CLUSTER_1_CONTEXT_NAME}
#  ${K8S_CLUSTER_2_CONTEXT_NAME}
#  ${OM_NAMESPACE}
#  ${CUSTOM_DOMAIN}
#  ${DNS_ZONE}

export S3_OPLOG_BUCKET_NAME=s3-oplog-store
export S3_SNAPSHOT_BUCKET_NAME=s3-snapshot-store

# If you use your own S3 storage - set the values accordingly.
# By default we install Minio to handle S3 storage and here are set the default credentials.
export S3_ENDPOINT="minio.tenant-tiny.svc.cluster.local"
export S3_ACCESS_KEY="console"
export S3_SECRET_KEY="console123"

export OPS_MANAGER_VERSION="8.0.5"
export APPDB_VERSION="8.0.5-ent"

# Global GCP load balancer resource names. Suffixed with K8S_CLUSTER_SUFFIX (as
# the clusters are) so concurrent runs in one project don't collide on these
# project-global names. The suffix is empty by default, so names stay clean.
export OM_LB_FIREWALL_NAME="fw-ops-manager-hc${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_HEALTHCHECK_NAME="om-healthcheck${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_BACKEND_SERVICE_NAME="om-backend-service${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_URL_MAP_NAME="om-url-map${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_PROXY_NAME="om-lb-proxy${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_CERT_NAME="om-certificate${K8S_CLUSTER_SUFFIX:-}"
export OM_LB_FORWARDING_RULE_NAME="om-forwarding-rule${K8S_CLUSTER_SUFFIX:-}"

export OPS_MANAGER_EXTERNAL_DOMAIN="opsmanager.${CUSTOM_DOMAIN}"
export APPDB_CLUSTER_0_EXTERNAL_DOMAIN="${K8S_CLUSTER_0}.${CUSTOM_DOMAIN}"
export APPDB_CLUSTER_1_EXTERNAL_DOMAIN="${K8S_CLUSTER_1}.${CUSTOM_DOMAIN}"
export APPDB_CLUSTER_2_EXTERNAL_DOMAIN="${K8S_CLUSTER_2}.${CUSTOM_DOMAIN}"
