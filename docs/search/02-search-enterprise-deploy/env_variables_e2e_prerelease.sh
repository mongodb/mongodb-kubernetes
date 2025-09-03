export K8S_CTX="${CLUSTER_NAME}"

export PRERELEASE_VERSION="1.4.0-prerelease-68b5c0bb136a0d0007a4c8a2"

export PRERELEASE_IMAGE_PULLSECRET="${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}"
export OPERATOR_ADDITIONAL_HELM_VALUES="registry.imagePullSecrets=prerelease-image-pullsecret"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/staging/helm-chart/mongodb-kubernetes:${PRERELEASE_VERSION}"

# we need project name with a timestamp (NAMESPACE in evg is randomized) to allow for cloud-qa cleanups
export OPS_MANAGER_PROJECT_NAME="${NAMESPACE}-${MDB_RESOURCE_NAME}"
export OPS_MANAGER_API_URL="${OM_BASE_URL}"
export OPS_MANAGER_API_USER="${OM_USER}"
export OPS_MANAGER_API_KEY="${OM_API_KEY}"
export OPS_MANAGER_ORG_ID="${OM_ORGID}"
