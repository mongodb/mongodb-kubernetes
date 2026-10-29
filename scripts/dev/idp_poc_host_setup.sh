#!/usr/bin/env bash
#
# Disclaimer: this file was mostly generated with AI, as part of a POC.
#
# IdP-federation POC: HOST-side setup.
#
# Runs ON the EVG host (Linux + Docker + the kind control-plane nodes), NOT on
# your Mac. Invoke it from your Mac with a single command:
#
#   scripts/dev/evg_host.sh cmd scripts/dev/idp_poc_host_setup.sh
#
# What it does (idempotently, safe to re-run):
#   1. Generates a self-signed IdP cert + PKCS12 keystore (SAN: central-idp).
#   2. Runs mock-oauth2-server as container `central-idp` on the `kind` Docker
#      network, published on the host loopback 127.0.0.1:4200 (so your Mac
#      reaches it through the SSH tunnel), and pins the IdP IP in each member
#      node's /etc/hosts (kind nodes do not use Docker's embedded DNS).
#   3. For each member kind node: writes an AuthenticationConfiguration with
#      the IdP CA inlined and adds --authentication-config to the kube-apiserver
#      static pod (removing legacy --oidc-* flags), then waits for restart.
#   4. Binds the shared operator identity RBAC on each member cluster.
#
# POC shortcuts (deliberate, NOT for production):
#   - self-signed IdP cert; the operator's helper skips IdP TLS verification
#   - the mock accepts any client_secret, so client authentication is not exercised
#   - RBAC binds cluster-admin; production would scope to the operator's member role
#   - the IdP IP pin breaks if the container is recreated without re-running this script
#
set -Eeou pipefail

# Prefer the operator-dev kubeconfig (kept current by make switch / evg_host.sh)
# over ~/.kube/config, which can retain stale API-server ports after clusters
# are recreated.
export KUBECONFIG="${KUBECONFIG:-${HOME}/.operator-dev/evg-host.kubeconfig}"

# --- POC identity / topology (override via env if needed) -------------------
IDP_NAME="${IDP_NAME:-central-idp}"
IDP_NETWORK="${IDP_NETWORK:-kind}"
IDP_PORT="${IDP_PORT:-4200}"
IDP_ISSUER="${IDP_ISSUER:-https://${IDP_NAME}:${IDP_PORT}/default}"
IDP_IMAGE="${IDP_IMAGE:-ghcr.io/navikt/mock-oauth2-server:6.1}"
KEYSTORE_PASS="${KEYSTORE_PASS:-password}"

# One shared operator identity. client_id becomes the token `sub`; the scope the
# helper requests becomes the token `aud`, which must match the configured
# audience (the former --oidc-client-id value).
OIDC_CLIENT_ID="${OIDC_CLIENT_ID:-mck-operator}"
OIDC_USERNAME_CLAIM="${OIDC_USERNAME_CLAIM:-sub}"
# The API server prefixes the username with "<issuer>#" to match the RBAC subject.
RBAC_SUBJECT="${IDP_ISSUER}#${OIDC_CLIENT_ID}"
RBAC_ROLE="${RBAC_ROLE:-cluster-admin}"
RBAC_BINDING_NAME="${RBAC_BINDING_NAME:-mck-idp-operator}"

read -ra MEMBER_NODES <<< "${MEMBER_NODES:-e2e-cluster-1-control-plane e2e-cluster-2-control-plane e2e-cluster-3-control-plane}"
read -ra MEMBER_CONTEXTS <<< "${MEMBER_CONTEXTS:-kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3}"

# Certs live OUTSIDE the repo tree so `evg_host.sh sync` (--delete) never wipes them.
CERT_DIR="${CERT_DIR:-${HOME}/.idp-poc/certs}"
# /etc/kubernetes/pki is mounted read-only into the kube-apiserver container,
# so this is a path the API server can read and watch. The CA is embedded in
# the file itself: AuthenticationConfiguration.certificateAuthority is PEM
# content, not a file path (unlike --oidc-ca-file).
AUTH_CONFIG_PATH_IN_NODE="/etc/kubernetes/pki/authentication-config.yaml"
APISERVER_MANIFEST="/etc/kubernetes/manifests/kube-apiserver.yaml"

echo "== IdP POC host setup =="
echo "issuer=${IDP_ISSUER} client_id=${OIDC_CLIENT_ID} rbac_subject=${RBAC_SUBJECT}"
echo "members=${MEMBER_CONTEXTS[*]}"

# --- 1. IdP cert + keystore -------------------------------------------------
mkdir -p "${CERT_DIR}"
# The structured AuthenticationConfiguration loader requires the CA certificate
# to carry keyCertSign; an old POC cert without a Key Usage extension is rejected
# with "data does not contain any valid RSA or ECDSA certificates".
if [[ ! -f "${CERT_DIR}/server.p12" ]] \
  || ! openssl x509 -in "${CERT_DIR}/cert.pem" -noout -text 2>/dev/null | grep -q 'X509v3 Key Usage'; then
  echo "Generating IdP cert/keystore in ${CERT_DIR} (SAN: ${IDP_NAME})"
  openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout "${CERT_DIR}/key.pem" -out "${CERT_DIR}/cert.pem" -days 365 \
    -subj "/CN=${IDP_NAME}" \
    -addext "subjectAltName=DNS:${IDP_NAME}" \
    -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign,cRLSign" \
    -addext "extendedKeyUsage=serverAuth"
  openssl pkcs12 -export -out "${CERT_DIR}/server.p12" \
    -inkey "${CERT_DIR}/key.pem" -in "${CERT_DIR}/cert.pem" -passout "pass:${KEYSTORE_PASS}"
else
  echo "Reusing existing IdP cert/keystore in ${CERT_DIR}"
fi
# The mock-oauth2-server image runs as UID 65532; a default umask leaves the
# keystore 0600 and the container fails with "Permission denied".
chmod 0644 "${CERT_DIR}/server.p12"

# --- 2. Run the IdP container ----------------------------------------------
echo "(Re)starting ${IDP_NAME} on network ${IDP_NETWORK}, published on 127.0.0.1:${IDP_PORT}"
docker rm -f "${IDP_NAME}" >/dev/null 2>&1 || true
docker run -d --name "${IDP_NAME}" \
  --network "${IDP_NETWORK}" \
  -p "127.0.0.1:${IDP_PORT}:${IDP_PORT}" \
  -e SERVER_PORT="${IDP_PORT}" \
  -e LOG_LEVEL="${IDP_LOG_LEVEL:-INFO}" \
  -v "${CERT_DIR}/server.p12:/app/server.p12" \
  -e JSON_CONFIG="{\"httpServer\":{\"type\":\"NettyWrapper\",\"ssl\":{\"keystoreFile\":\"/app/server.p12\",\"keystoreType\":\"PKCS12\",\"keystorePassword\":\"${KEYSTORE_PASS}\",\"keyPassword\":\"${KEYSTORE_PASS}\"}}}" \
  "${IDP_IMAGE}"

if [[ "$(docker inspect -f '{{.State.Running}}' "${IDP_NAME}" 2>/dev/null)" != "true" ]]; then
  echo "ERROR: ${IDP_NAME} exited on startup:" >&2
  docker logs --tail 30 "${IDP_NAME}" >&2 || true
  exit 1
fi

# The member kube-apiservers run in the node containers' network namespace.
# Their resolv.conf points at the Docker gateway (172.18.0.1), not Docker's
# embedded DNS (127.0.0.11), so container-name DNS does not work from a node.
# Pin the IdP's address in each node's /etc/hosts so the API server can resolve
# the issuer host.
IDP_IP="$(docker inspect -f "{{ (index .NetworkSettings.Networks \"${IDP_NETWORK}\").IPAddress }}" "${IDP_NAME}")"
if [[ -z "${IDP_IP}" ]]; then
  echo "ERROR: could not determine ${IDP_NAME} address on network ${IDP_NETWORK}" >&2
  exit 1
fi
for node in "${MEMBER_NODES[@]}"; do
  # /etc/hosts is a bind mount in a kind node, so `sed -i` cannot rename over
  # it; rewrite the file in place instead.
  docker exec "${node}" sh -c \
    "grep -vE '[[:space:]]${IDP_NAME}\$' /etc/hosts > /tmp/idp-hosts || true; cat /tmp/idp-hosts > /etc/hosts; echo '${IDP_IP} ${IDP_NAME}' >> /etc/hosts"
done
echo "Pinned ${IDP_NAME}=${IDP_IP} in member nodes' /etc/hosts"

echo "Waiting for IdP discovery endpoint..."
idp_ready=false
for _ in {1..30}; do
  if curl -sk "https://127.0.0.1:${IDP_PORT}/default/.well-known/openid-configuration" >/dev/null 2>&1; then
    echo "IdP is up."
    idp_ready=true
    break
  fi
  sleep 1
done
if [[ "${idp_ready}" != "true" ]]; then
  echo "ERROR: IdP did not become ready on port ${IDP_PORT}:" >&2
  docker logs --tail 30 "${IDP_NAME}" >&2 || true
  exit 1
fi
for node in "${MEMBER_NODES[@]}"; do
  if ! docker exec "${node}" getent hosts "${IDP_NAME}" >/dev/null 2>&1; then
    echo "ERROR: ${node} cannot resolve ${IDP_NAME}" >&2
    exit 1
  fi
done

# --- 3. Trust the IdP on each member kube-apiserver ------------------------
# AuthenticationConfiguration v1 is the current API; v1beta1 is what 1.30-1.33
# use. The repo's current kind images are 1.35-1.37, so this normally picks v1.
# Read the version from the node's kubelet, not the API server: a previous
# failed run may have left the API server crash-looping, and this script must
# still be able to repair the cluster.
server_minor="$(docker exec "${MEMBER_NODES[0]}" kubelet --version 2>/dev/null \
  | sed -n 's/.*v1\.\([0-9][0-9]*\)\..*/\1/p')"
case "${server_minor}" in
  ''|*[!0-9]*)
    echo "ERROR: could not determine the Kubernetes server minor version" >&2
    exit 1
    ;;
esac
if (( server_minor >= 34 )); then
  AUTH_CONFIG_API_VERSION="apiserver.config.k8s.io/v1"
elif (( server_minor >= 30 )); then
  AUTH_CONFIG_API_VERSION="apiserver.config.k8s.io/v1beta1"
else
  echo "ERROR: --authentication-config requires Kubernetes 1.30+ (server is 1.${server_minor}); use the legacy --oidc-* flags instead" >&2
  exit 1
fi

AUTH_CONFIG_HOST="$(mktemp)"
trap 'rm -f "${AUTH_CONFIG_HOST}"' EXIT
{
  cat <<EOF
apiVersion: ${AUTH_CONFIG_API_VERSION}
kind: AuthenticationConfiguration
jwt:
  - issuer:
      url: "${IDP_ISSUER}"
      audiences:
        - "${OIDC_CLIENT_ID}"
      certificateAuthority: |
EOF
  sed 's/^/        /' "${CERT_DIR}/cert.pem"
  cat <<EOF
    claimMappings:
      username:
        claim: "${OIDC_USERNAME_CLAIM}"
        prefix: "${IDP_ISSUER}#"
EOF
} > "${AUTH_CONFIG_HOST}"

for node in "${MEMBER_NODES[@]}"; do
  echo "-- ${node}: installing AuthenticationConfiguration (CA inlined)"
  docker cp "${AUTH_CONFIG_HOST}" "${node}:${AUTH_CONFIG_PATH_IN_NODE}"
  docker exec "${node}" chmod 0644 "${AUTH_CONFIG_PATH_IN_NODE}"

  if docker exec "${node}" grep -q -- "--authentication-config=${AUTH_CONFIG_PATH_IN_NODE}" "${APISERVER_MANIFEST}" \
    && ! docker exec "${node}" grep -qE '^[[:space:]]*-[[:space:]]+--oidc-' "${APISERVER_MANIFEST}"; then
    echo "   authentication-config already present; skipping manifest patch"
  else
    # Back up the pristine manifest once, outside the manifests dir so kubelet
    # ignores it, making the patch reversible.
    docker exec "${node}" sh -c \
      "[ -f /etc/kubernetes/kube-apiserver.yaml.idp-bak ] || cp ${APISERVER_MANIFEST} /etc/kubernetes/kube-apiserver.yaml.idp-bak"
    tmp="$(mktemp)"
    docker cp "${node}:${APISERVER_MANIFEST}" "${tmp}"
    # Drop legacy OIDC flags and any stale authentication-config line so the
    # rewrite is idempotent and the two mechanisms are never combined.
    grep -vE '^[[:space:]]*-[[:space:]]+--(oidc-[a-z-]+|authentication-config)=' "${tmp}" > "${tmp}.clean" || true
    awk -v cfg="${AUTH_CONFIG_PATH_IN_NODE}" '
      /^[[:space:]]*- kube-apiserver$/ && !done {
        prefix = substr($0, 1, index($0, "-") - 1)
        print
        print prefix "- --authentication-config=" cfg
        done = 1
        next
      }
      { print }
    ' "${tmp}.clean" > "${tmp}.patched"
    grep -q -- '--authentication-config=' "${tmp}.patched" \
      || { echo "   ERROR: failed to insert --authentication-config into ${node} manifest" >&2; exit 1; }
    if grep -qE '^[[:space:]]*-[[:space:]]+--oidc-' "${tmp}.patched"; then
      echo "   ERROR: legacy --oidc-* flags remain in ${node} manifest" >&2
      exit 1
    fi
    docker cp "${tmp}.patched" "${node}:${APISERVER_MANIFEST}"
    rm -f "${tmp}" "${tmp}.clean" "${tmp}.patched"
    echo "   wrote ${AUTH_CONFIG_PATH_IN_NODE} and patched manifest; kubelet will restart kube-apiserver"
  fi
done

echo "Waiting for member kube-apiservers to come back..."
for ctx in "${MEMBER_CONTEXTS[@]}"; do
  ready=false
  for _ in {1..60}; do
    if kubectl --context "${ctx}" get --raw='/readyz' >/dev/null 2>&1; then
      echo "   ${ctx} ready"
      ready=true
      break
    fi
    sleep 2
  done
  if [[ "${ready}" != "true" ]]; then
    echo "   WARNING: ${ctx} not ready within ~120s. If kube-apiserver is crash-looping, restore the backup:" >&2
    echo "     docker exec ${ctx#kind-}-control-plane cp /etc/kubernetes/kube-apiserver.yaml.idp-bak ${APISERVER_MANIFEST}" >&2
  fi
done

# --- 4. RBAC for the shared operator identity on each member ---------------
for ctx in "${MEMBER_CONTEXTS[@]}"; do
  echo "-- ${ctx}: binding ${RBAC_ROLE} to ${RBAC_SUBJECT}"
  kubectl --context "${ctx}" create clusterrolebinding "${RBAC_BINDING_NAME}" \
    --clusterrole="${RBAC_ROLE}" --user="${RBAC_SUBJECT}" \
    --dry-run=client -o yaml | kubectl --context "${ctx}" apply -f -
done

echo
echo "== Host setup complete =="
echo "IdP issuer:        ${IDP_ISSUER}"
echo "Token endpoint:    ${IDP_ISSUER}/token"
echo "API server auth:   ${AUTH_CONFIG_API_VERSION} via --authentication-config"
echo "Auth config file: ${AUTH_CONFIG_PATH_IN_NODE}"
echo "RBAC subject:      ${RBAC_SUBJECT}  (bound to ${RBAC_ROLE} on each member)"
echo
echo "Next, on your Mac:"
echo "  1. if not already done, prepare from the Mac with the member tunnel up:"
echo "       make switch context=e2e_static_multi_cluster_kind && make prepare-local-e2e"
echo "  2. scripts/dev/idp_poc_local_setup.sh                          # swap member kubeconfig users to exec"
echo "  3. add '127.0.0.1 ${IDP_NAME}' to /etc/hosts                    # so the helper's Host header matches the issuer"
echo "  4. scripts/dev/evg_host.sh tunnel                              # member API servers (your normal tunnel)"
echo "  5. scripts/dev/evg_host.sh ssh -N -L ${IDP_PORT}:127.0.0.1:${IDP_PORT}  # IdP port, in a SECOND terminal"
echo "  6. set the operator context on the host kubeconfig copy (kind-e2e-operator)"
echo "  7. run 'kubectl auth whoami' against each member (pre-check)"
echo "  8. run the operator in your IDE (LOCAL_OPERATOR=true)"
