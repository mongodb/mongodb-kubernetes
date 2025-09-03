export K8S_CTX="${CLUSTER_NAME}"

export PRERELEASE_VERSION="1.4.0-prerelease-68b5c0bb136a0d0007a4c8a2"

export PRERELEASE_IMAGE_PULLSECRET="${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}"
export OPERATOR_ADDITIONAL_HELM_VALUES="registry.imagePullSecrets=prerelease-image-pullsecret"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/staging/helm-chart/mongodb-kubernetes:${PRERELEASE_VERSION}"
