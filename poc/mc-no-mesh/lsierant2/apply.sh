#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

C0="${CLUSTER0_CONTEXT:-default/api-kubehosted-9h9y-p1-openshiftapps-com:6443/admin-kubehosted}"
C1="${CLUSTER1_CONTEXT:-/api-u5p3c2n7r3a9k9m-scnn-p1-openshiftapps-com:6443/kubehosted-admin}"
NS="${POC_NAMESPACE:-lsierant2}"
REPO="${REPO_PATH:-/Users/lukasz.sierant/mdb/mongodb-kubernetes}"
SOURCE_NAMESPACE="${SOURCE_NAMESPACE:-mongodb}"
SOURCE_OPS_MANAGER_CONFIGMAP="${SOURCE_OPS_MANAGER_CONFIGMAP:-my-project}"
SOURCE_CREDENTIALS_SECRET="${SOURCE_CREDENTIALS_SECRET:-my-credentials}"
SOURCE_IMAGE_PULL_SECRET="${SOURCE_IMAGE_PULL_SECRET:-image-registries-secret}"
OPS_MANAGER_CONFIGMAP="${OPS_MANAGER_CONFIGMAP:-lsierant2-project}"
CREDENTIALS_SECRET="${CREDENTIALS_SECRET:-my-credentials}"
IMAGE_PULL_SECRET="${IMAGE_PULL_SECRET:-image-registries-secret}"
OPS_MANAGER_PROJECT_NAME="${OPS_MANAGER_PROJECT_NAME:-mongodb-lsierant2-mc-envoy-dns-poc}"
OPERATOR_IMAGE_TAG="${OPERATOR_IMAGE_TAG:-remote-external-pod-service-v1}"
BUILD_OPERATOR_IMAGE="${BUILD_OPERATOR_IMAGE:-true}"
COREDNS_IMAGE="${COREDNS_IMAGE:-quay.io/openshift-release-dev/ocp-v4.0-art-dev@sha256:f02816882ad485fd1586f9c3d4573587feb62bf1c30a64689ce6157a13f1aced}"
CLUSTER0_DOMAIN="${CLUSTER0_DOMAIN:-c0.lsierant2.mc-no-mesh.test}"
CLUSTER1_DOMAIN="${CLUSTER1_DOMAIN:-c1.lsierant2.mc-no-mesh.test}"
CLUSTER0_ROUTER_HOST="${CLUSTER0_ROUTER_HOST:-}"
CLUSTER1_ROUTER_HOST="${CLUSTER1_ROUTER_HOST:-}"
DOCKER_AUTH_DIR="$SCRIPT_DIR/.docker-auth"

OPS_MANAGER_BASE_URL=""
OPS_MANAGER_ORG_ID=""
CLUSTER1_API_HOST=""
CLUSTER1_API_PORT=""
CLUSTER0_DNS_IP=""
CLUSTER1_DNS_IP=""
MDB_0_0_IP=""
MDB_1_0_IP=""
MDB_1_1_IP=""
REMOTE_ROUTER_HOST=""
DNS_IP=""
CONFIG_HASH="pending"

export NS REPO OPS_MANAGER_CONFIGMAP CREDENTIALS_SECRET IMAGE_PULL_SECRET
export OPS_MANAGER_PROJECT_NAME OPERATOR_IMAGE_TAG CLUSTER0_DOMAIN CLUSTER1_DOMAIN
export COREDNS_IMAGE
export OPS_MANAGER_BASE_URL OPS_MANAGER_ORG_ID CLUSTER1_API_HOST CLUSTER1_API_PORT
export CLUSTER0_DNS_IP CLUSTER1_DNS_IP MDB_0_0_IP MDB_1_0_IP MDB_1_1_IP
export REMOTE_ROUTER_HOST DNS_IP CONFIG_HASH

die() {
  echo "error: $*" >&2
  exit 1
}

cleanup_runtime() {
  unset C0_TOKEN C1_TOKEN C0_CA password
  rm -rf -- "$DOCKER_AUTH_DIR"
}
trap cleanup_runtime EXIT

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

context_exists() {
  kubectl config view -o json | jq -e --arg context "$1" \
    '.contexts[] | select(.name == $context)' >/dev/null
}

require_can_i() {
  local context="$1" namespace="$2" verb="$3" resource="$4"
  local answer
  if [[ -n "$namespace" ]]; then
    answer="$(kubectl --context="$context" -n "$namespace" auth can-i "$verb" "$resource")"
  else
    answer="$(kubectl --context="$context" auth can-i "$verb" "$resource")"
  fi
  [[ "$answer" == "yes" ]] || die "context $context cannot $verb $resource in ${namespace:-cluster scope}"
}

render_manifest() {
  python3 - "$1" <<'PY'
import os
import pathlib
import sys

text = pathlib.Path(sys.argv[1]).read_text()
keys = [
    "NAMESPACE",
    "IMAGE_PULL_SECRET",
    "OPERATOR_IMAGE_TAG",
    "COREDNS_IMAGE",
    "OPS_MANAGER_CONFIGMAP",
    "OPS_MANAGER_BASE_URL",
    "OPS_MANAGER_ORG_ID",
    "OPS_MANAGER_PROJECT_NAME",
    "CREDENTIALS_SECRET",
    "CLUSTER0_DOMAIN",
    "CLUSTER1_DOMAIN",
    "CLUSTER1_API_HOST",
    "CLUSTER1_API_PORT",
    "CLUSTER0_DNS_IP",
    "CLUSTER1_DNS_IP",
    "MDB_0_0_IP",
    "MDB_1_0_IP",
    "MDB_1_1_IP",
    "REMOTE_ROUTER_HOST",
    "DNS_IP",
    "CONFIG_HASH",
]
values = {"NAMESPACE": os.environ["NS"]}
values.update({key: os.environ.get(key, "") for key in keys if key != "NAMESPACE"})
for key, value in values.items():
    text = text.replace(f"__{key}__", value)
sys.stdout.write(text)
PY
}

apply_manifest() {
  local context="$1" manifest="$2"
  render_manifest "$SCRIPT_DIR/$manifest" \
    | kubectl --context="$context" -n "$NS" apply -f - >/dev/null
}

copy_object() {
  local source_context="$1" source_namespace="$2" target_context="$3"
  local kind="$4" source_name="$5" target_name="${6:-$5}"
  kubectl --context="$source_context" -n "$source_namespace" get "$kind" "$source_name" -o json \
    | jq --arg namespace "$NS" --arg name "$target_name" '
        del(
          .metadata.uid,
          .metadata.resourceVersion,
          .metadata.creationTimestamp,
          .metadata.managedFields,
          .metadata.ownerReferences,
          .metadata.annotations."kubectl.kubernetes.io/last-applied-configuration"
        )
        | .metadata.namespace = $namespace
        | .metadata.name = $name
      ' \
    | kubectl --context="$target_context" -n "$NS" apply -f - >/dev/null
}

wait_for_service_account_token() {
  local context="$1"
  for _ in $(seq 1 60); do
    local keys
    keys="$(
      kubectl --context="$context" -n "$NS" get secret mc-no-mesh-operator-token \
        -o json 2>/dev/null | jq -r '(.data // {}) | keys | join(",")'
    )"
    if [[ "$keys" == *token* && "$keys" == *ca.crt* ]]; then
      return
    fi
    sleep 2
  done
  die "service-account token was not populated in context $context"
}

wait_for_route() {
  local context="$1" route="$2"
  for _ in $(seq 1 60); do
    if [[ "$(
      kubectl --context="$context" -n "$NS" get route "$route" \
        -o jsonpath='{.status.ingress[0].conditions[?(@.type=="Admitted")].status}' 2>/dev/null
    )" == "True" ]]; then
      return
    fi
    sleep 2
  done
  die "Route $route was not admitted in context $context"
}

wait_for_phase() {
  local context="$1" resource="$2" expected="$3"
  for _ in $(seq 1 180); do
    if [[ "$(
      kubectl --context="$context" -n "$NS" get "$resource" \
        -o jsonpath='{.status.phase}' 2>/dev/null
    )" == "$expected" ]]; then
      return
    fi
    sleep 5
  done
  die "$resource did not reach phase $expected in context $context"
}

wait_for_mdb_running() {
  for _ in $(seq 1 240); do
    local state phase generation observed
    state="$(
      kubectl --context="$C0" -n "$NS" get mongodbmulticluster mdb -o json 2>/dev/null \
        | jq -r '[.status.phase, (.metadata.generation | tostring), (.status.observedGeneration | tostring)] | @tsv'
    )"
    if [[ "$state" == $'Running\t'* ]]; then
      IFS=$'\t' read -r phase generation observed <<<"$state"
      if [[ "$generation" == "$observed" ]]; then
        return
      fi
    fi
    sleep 5
  done
  die "mongodbmulticluster/mdb did not reach Running with the current generation"
}

service_ip() {
  kubectl --context="$1" -n "$NS" get service "$2" -o jsonpath='{.spec.clusterIP}'
}

manifest_hash() {
  CONFIG_HASH="pending" render_manifest "$1" | openssl dgst -sha256 | awk '{print $2}'
}

for command in kubectl oc helm jq python3 openssl awk; do
  require_command "$command"
done
if [[ "$BUILD_OPERATOR_IMAGE" == "true" ]]; then
  require_command docker
  docker buildx version >/dev/null 2>&1 || die "Docker buildx is required"
elif [[ "$BUILD_OPERATOR_IMAGE" != "false" ]]; then
  die "BUILD_OPERATOR_IMAGE must be true or false"
fi

[[ -d "$REPO/helm_chart" ]] || die "Helm chart not found under REPO_PATH=$REPO"
[[ -f "$REPO/docker/mongodb-kubernetes-operator/Dockerfile" ]] \
  || die "operator Dockerfile not found under REPO_PATH=$REPO"
context_exists "$C0" || die "kubectl context not found: $C0"
context_exists "$C1" || die "kubectl context not found: $C1"
kubectl --context="$C0" version --request-timeout=20s >/dev/null
kubectl --context="$C1" version --request-timeout=20s >/dev/null

if ! kubectl --context="$C0" get namespace "$NS" >/dev/null 2>&1; then
  kubectl --context="$C0" create namespace "$NS" >/dev/null
fi
if ! kubectl --context="$C1" -n "$NS" get serviceaccount default >/dev/null 2>&1; then
  require_can_i "$C1" "" create projectrequests.project.openshift.io
  oc --context="$C1" new-project "$NS" >/dev/null
fi

for resource in serviceaccounts secrets roles.rbac.authorization.k8s.io rolebindings.rbac.authorization.k8s.io deployments.apps services routes.route.openshift.io issuers.cert-manager.io certificates.cert-manager.io mongodbmulticluster.mongodb.com mongodbusers.mongodb.com; do
  require_can_i "$C0" "$NS" create "$resource"
done
for resource in serviceaccounts secrets roles.rbac.authorization.k8s.io rolebindings.rbac.authorization.k8s.io deployments.apps services routes.route.openshift.io; do
  require_can_i "$C1" "$NS" create "$resource"
done

kubectl --context="$C0" get crd mongodbmulticluster.mongodb.com >/dev/null
kubectl --context="$C0" -n "$SOURCE_NAMESPACE" get configmap "$SOURCE_OPS_MANAGER_CONFIGMAP" >/dev/null
kubectl --context="$C0" -n "$SOURCE_NAMESPACE" get secret "$SOURCE_CREDENTIALS_SECRET" >/dev/null
kubectl --context="$C0" -n "$SOURCE_NAMESPACE" get secret "$SOURCE_IMAGE_PULL_SECRET" >/dev/null

OPS_MANAGER_BASE_URL="$(
  kubectl --context="$C0" -n "$SOURCE_NAMESPACE" get configmap "$SOURCE_OPS_MANAGER_CONFIGMAP" \
    -o jsonpath='{.data.baseUrl}'
)"
OPS_MANAGER_ORG_ID="$(
  kubectl --context="$C0" -n "$SOURCE_NAMESPACE" get configmap "$SOURCE_OPS_MANAGER_CONFIGMAP" \
    -o jsonpath='{.data.orgId}'
)"
[[ -n "$OPS_MANAGER_BASE_URL" && -n "$OPS_MANAGER_ORG_ID" ]] \
  || die "source Ops Manager ConfigMap is missing baseUrl or orgId"
export OPS_MANAGER_BASE_URL OPS_MANAGER_ORG_ID

C1_CLUSTER="$(kubectl config view -o json | jq -r --arg context "$C1" '.contexts[] | select(.name == $context) | .context.cluster')"
C1_SERVER="$(kubectl config view -o json | jq -r --arg cluster "$C1_CLUSTER" '.clusters[] | select(.name == $cluster) | .cluster.server')"
read -r CLUSTER1_API_HOST CLUSTER1_API_PORT < <(
  python3 - "$C1_SERVER" <<'PY'
from urllib.parse import urlparse
import sys

url = urlparse(sys.argv[1])
print(url.hostname, url.port or 443)
PY
)
export CLUSTER1_API_HOST CLUSTER1_API_PORT

copy_object "$C0" "$SOURCE_NAMESPACE" "$C0" secret "$SOURCE_CREDENTIALS_SECRET" "$CREDENTIALS_SECRET"
copy_object "$C0" "$SOURCE_NAMESPACE" "$C0" secret "$SOURCE_IMAGE_PULL_SECRET" "$IMAGE_PULL_SECRET"
copy_object "$C0" "$SOURCE_NAMESPACE" "$C1" secret "$SOURCE_IMAGE_PULL_SECRET" "$IMAGE_PULL_SECRET"
apply_manifest "$C0" 04-ops-manager-project.yaml

apply_manifest "$C0" 00-cluster0-rbac.yaml
apply_manifest "$C1" 01-cluster1-rbac.yaml
wait_for_service_account_token "$C0"
wait_for_service_account_token "$C1"

C0_CLUSTER="$(kubectl config view -o json | jq -r --arg context "$C0" '.contexts[] | select(.name == $context) | .context.cluster')"
C0_API="$(kubectl config view -o json | jq -r --arg cluster "$C0_CLUSTER" '.clusters[] | select(.name == $cluster) | .cluster.server')"
C0_CA="$(kubectl --context="$C0" -n "$NS" get secret mc-no-mesh-operator-token -o jsonpath='{.data.ca\.crt}')"
C0_TOKEN="$(kubectl --context="$C0" -n "$NS" get secret mc-no-mesh-operator-token -o jsonpath='{.data.token}' | base64 -d)"
C1_TOKEN="$(kubectl --context="$C1" -n "$NS" get secret mc-no-mesh-operator-token -o jsonpath='{.data.token}' | base64 -d)"

jq -n \
  --arg c0api "$C0_API" --arg c0ca "$C0_CA" \
  --arg c0token "$C0_TOKEN" --arg c1token "$C1_TOKEN" --arg namespace "$NS" \
  '{
    apiVersion:"v1",
    kind:"Config",
    currentContext:"cluster0",
    clusters:[
      {name:"cluster0",cluster:{server:$c0api,"certificate-authority-data":$c0ca}},
      {name:"cluster1",cluster:{server:("http://cluster1-api-proxy."+$namespace+".svc.cluster.local:8080")}}
    ],
    contexts:[
      {name:"cluster0",context:{cluster:"cluster0",namespace:$namespace,user:"cluster0"}},
      {name:"cluster1",context:{cluster:"cluster1",namespace:$namespace,user:"cluster1"}}
    ],
    users:[
      {name:"cluster0",user:{token:$c0token}},
      {name:"cluster1",user:{token:$c1token}}
    ]
  }' \
  | kubectl --context="$C0" -n "$NS" create secret generic mongodb-enterprise-operator-multi-cluster-kubeconfig \
      --from-file=kubeconfig=/dev/stdin --dry-run=client -o yaml \
  | kubectl --context="$C0" -n "$NS" apply -f - >/dev/null

kubectl --context="$C1" -n "$NS" get secret mc-no-mesh-operator-token -o json \
  | jq --arg namespace "$NS" '{
      apiVersion:"v1",
      kind:"Secret",
      metadata:{name:"cluster1-api-proxy-credentials",namespace:$namespace},
      type:"Opaque",
      data:{"ca.crt":.data["ca.crt"],token:.data.token}
    }' \
  | kubectl --context="$C0" -n "$NS" apply -f - >/dev/null
unset C0_TOKEN C1_TOKEN C0_CA

apply_manifest "$C0" 03-member-list.yaml
CONFIG_HASH="$(manifest_hash "$SCRIPT_DIR/15-cluster1-api-proxy.yaml")"
export CONFIG_HASH
apply_manifest "$C0" 15-cluster1-api-proxy.yaml
kubectl --context="$C0" -n "$NS" rollout status deployment/cluster1-api-proxy --timeout=4m

if ! oc --context="$C0" -n "$NS" get imagestream mc-no-mesh-operator >/dev/null 2>&1; then
  apply_manifest "$C0" 13-custom-operator-image.yaml
fi
if [[ "$BUILD_OPERATOR_IMAGE" == "true" ]]; then
  PUBLIC_REGISTRY="$(oc registry info --public --context="$C0")"
  rm -rf -- "$DOCKER_AUTH_DIR"
  mkdir -p "$DOCKER_AUTH_DIR/cli-plugins"
  if [[ -x /Applications/Docker.app/Contents/Resources/cli-plugins/docker-buildx ]]; then
    ln -s /Applications/Docker.app/Contents/Resources/cli-plugins/docker-buildx \
      "$DOCKER_AUTH_DIR/cli-plugins/docker-buildx"
  fi
  oc registry login --context="$C0" --registry="$PUBLIC_REGISTRY" \
    --to="$DOCKER_AUTH_DIR/config.json" >/dev/null
  docker --config "$DOCKER_AUTH_DIR" buildx build \
    --platform linux/amd64 \
    --file "$REPO/docker/mongodb-kubernetes-operator/Dockerfile" \
    --build-arg version=1.10.1-remote-external-pod-service-poc \
    --build-arg log_automation_config_diff=false \
    --build-arg use_race=false \
    --tag "$PUBLIC_REGISTRY/$NS/mc-no-mesh-operator:$OPERATOR_IMAGE_TAG" \
    --push "$REPO"
  rm -rf -- "$DOCKER_AUTH_DIR"
else
  oc --context="$C0" -n "$NS" get imagestreamtag \
    "mc-no-mesh-operator:$OPERATOR_IMAGE_TAG" >/dev/null \
    || die "operator ImageStreamTag is missing and BUILD_OPERATOR_IMAGE=false"
fi

if ! kubectl --context="$C0" get crd mongodbmulticluster.mongodb.com -o json \
  | jq -e '.spec.versions[] | select(.storage == true) | .schema.openAPIV3Schema.properties.spec.properties | has("remoteDuplicatePodService")' >/dev/null; then
  kubectl --context="$C0" patch crd mongodbmulticluster.mongodb.com \
    --type=json --patch-file "$SCRIPT_DIR/14-remote-duplicate-pod-service-crd-patch.json" >/dev/null
fi

OPERATOR_PULL_SECRET="$(
  kubectl --context="$C0" -n "$NS" get serviceaccount mc-no-mesh-operator -o json \
    | jq -r '.imagePullSecrets[].name | select(contains("dockercfg"))' \
    | head -1
)"
[[ -n "$OPERATOR_PULL_SECRET" ]] || die "OpenShift registry pull Secret was not attached to the operator ServiceAccount"
DESIRED_OPERATOR_IMAGE="image-registry.openshift-image-registry.svc:5000/$NS/mc-no-mesh-operator:$OPERATOR_IMAGE_TAG"
helm upgrade --install mc-no-mesh-lsierant2 "$REPO/helm_chart" \
  --kube-context="$C0" --namespace "$NS" --create-namespace=false --skip-crds \
  -f <(render_manifest "$SCRIPT_DIR/02-operator-values.yaml") --timeout 5m
kubectl --context="$C0" -n "$NS" patch deployment mc-no-mesh-operator --type=merge \
  -p "{\"spec\":{\"template\":{\"spec\":{\"imagePullSecrets\":[{\"name\":\"$IMAGE_PULL_SECRET\"},{\"name\":\"$OPERATOR_PULL_SECRET\"}]}}}}" >/dev/null
kubectl --context="$C0" -n "$NS" rollout status deployment/mc-no-mesh-operator --timeout=4m

apply_manifest "$C0" 05-certificates.yaml
kubectl --context="$C0" -n "$NS" wait --for=condition=Ready \
  certificate/mc-no-mesh-ca certificate/mc-no-mesh-mdb certificate/mc-no-mesh-mdb-agent --timeout=4m
kubectl --context="$C0" -n "$NS" get secret mc-no-mesh-ca-key-pair -o jsonpath='{.data.tls\.crt}' \
  | base64 -d \
  | kubectl --context="$C0" -n "$NS" create configmap mc-no-mesh-ca \
      --from-file=ca-pem=/dev/stdin --dry-run=client -o yaml \
  | kubectl --context="$C0" -n "$NS" apply -f - >/dev/null
for secret_name in mc-no-mesh-mdb-cert mc-no-mesh-mdb-agent-certs; do
  copy_object "$C0" "$NS" "$C1" secret "$secret_name"
done
copy_object "$C0" "$NS" "$C1" configmap mc-no-mesh-ca

apply_manifest "$C0" 06-precreate-external-services.yaml
apply_manifest "$C1" 06-precreate-external-services.yaml
CLUSTER0_DNS_IP="$(service_ip "$C0" mc-no-mesh-dns)"
CLUSTER1_DNS_IP="$(service_ip "$C1" mc-no-mesh-dns)"
export CLUSTER0_DNS_IP CLUSTER1_DNS_IP
[[ "$CLUSTER0_DNS_IP" != "None" && "$CLUSTER1_DNS_IP" != "None" ]] \
  || die "namespace DNS Services did not receive ClusterIPs"

apply_manifest "$C0" 07-cluster0-routes.yaml
apply_manifest "$C1" 08-cluster1-routes.yaml
wait_for_route "$C0" mdb-0-0-external
wait_for_route "$C1" mdb-1-0-external
wait_for_route "$C1" mdb-1-1-external
if [[ -z "$CLUSTER0_ROUTER_HOST" ]]; then
  CLUSTER0_ROUTER_HOST="$(
    kubectl --context="$C0" -n "$NS" get route mdb-0-0-external \
      -o jsonpath='{.status.ingress[0].routerCanonicalHostname}'
  )"
fi
if [[ -z "$CLUSTER1_ROUTER_HOST" ]]; then
  CLUSTER1_ROUTER_HOST="$(
    kubectl --context="$C1" -n "$NS" get route mdb-1-0-external \
      -o jsonpath='{.status.ingress[0].routerCanonicalHostname}'
  )"
fi
[[ -n "$CLUSTER0_ROUTER_HOST" && -n "$CLUSTER1_ROUTER_HOST" ]] \
  || die "router canonical hostname discovery failed"

deploy_relay_dns() {
  local context="$1" remote_router="$2"
  MDB_0_0_IP="$(service_ip "$context" mdb-0-0-svc-external)"
  MDB_1_0_IP="$(service_ip "$context" mdb-1-0-svc-external)"
  MDB_1_1_IP="$(service_ip "$context" mdb-1-1-svc-external)"
  REMOTE_ROUTER_HOST="$remote_router"
  CONFIG_HASH="$(manifest_hash "$SCRIPT_DIR/09-relay-dns.yaml")"
  export MDB_0_0_IP MDB_1_0_IP MDB_1_1_IP REMOTE_ROUTER_HOST CONFIG_HASH
  apply_manifest "$context" 09-relay-dns.yaml
  kubectl --context="$context" -n "$NS" rollout status deployment/mc-no-mesh-relay --timeout=5m
}

deploy_relay_dns "$C0" "$CLUSTER1_ROUTER_HOST"
deploy_relay_dns "$C1" "$CLUSTER0_ROUTER_HOST"

apply_manifest "$C0" 10-mongodbmulticluster.yaml
if ! kubectl --context="$C0" -n "$NS" get secret mc-no-mesh-user-password >/dev/null 2>&1; then
  password="$(openssl rand -base64 24 | tr -d '\n')"
  kubectl --context="$C0" -n "$NS" create secret generic mc-no-mesh-user-password \
    --from-literal=password="$password" >/dev/null
  unset password
fi
copy_object "$C0" "$NS" "$C1" secret mc-no-mesh-user-password
apply_manifest "$C0" 11-poc-user.yaml

DNS_IP="$CLUSTER0_DNS_IP"
export DNS_IP
apply_manifest "$C0" 12-mongosh-client.yaml
DNS_IP="$CLUSTER1_DNS_IP"
export DNS_IP
apply_manifest "$C1" 12-mongosh-client.yaml
kubectl --context="$C0" -n "$NS" wait --for=condition=Ready pod/mc-no-mesh-client --timeout=5m
kubectl --context="$C1" -n "$NS" wait --for=condition=Ready pod/mc-no-mesh-client --timeout=5m

wait_for_mdb_running
wait_for_phase "$C0" mongodbuser/mc-no-mesh-user Updated
kubectl --context="$C0" -n "$NS" get mongodbmulticluster mdb \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase,VERSION:.spec.version --no-headers
kubectl --context="$C0" -n "$NS" get mongodbuser mc-no-mesh-user \
  -o custom-columns=NAME:.metadata.name,PHASE:.status.phase --no-headers
