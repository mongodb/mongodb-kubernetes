export K8S_CTX="<kube context name>"

# specify prerelease version
export PRERELEASE_VERSION="1.4.0-prerelease-68b9584ac0a75a00070384a0" # mongodb search version used is 1.53.0-95-g8411af86f

# base64 of docker's config.json containing credentials to quay.io
export PRERELEASE_IMAGE_PULLSECRET="<base64 of dockerconfigjson>"

# this parameter is passed to the helm install to instruct the operator to
# configure every pod with prerelease-image-pullsecret
export OPERATOR_ADDITIONAL_HELM_VALUES="registry.imagePullSecrets=prerelease-image-pullsecret"
export OPERATOR_HELM_CHART="oci://quay.io/mongodb/staging/helm-chart/mongodb-kubernetes:${PRERELEASE_VERSION}"

# OM/CM's project name to be used to manage mongodb replica set
export OPS_MANAGER_PROJECT_NAME="<arbitrary project name>"

# URL to Cloud Manager or Ops Manager instance
export OPS_MANAGER_API_URL="https://cloud-qa.mongodb.com"

# The API key can be an Org Owner - the operator can create the project automatically.
# The API key can also be created in a particular project that was created manually with the Project Owner scope .
export OPS_MANAGER_API_USER="abcdefg"
export OPS_MANAGER_API_KEY="00000-abcd-efgh-1111-12345678"
export OPS_MANAGER_ORG_ID="62a73abcdefgh12345678"

