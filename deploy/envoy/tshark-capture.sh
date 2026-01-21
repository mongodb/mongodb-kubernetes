#!/bin/bash
# Script to capture and decrypt TLS traffic in Envoy pod using tshark
# This uses kubectl debug with an ephemeral container

set -euo pipefail

NAMESPACE="${1:-ls}"
OUTPUT_FILE="${2:-envoy-capture-$(date +%Y%m%d-%H%M%S).pcap}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}=== TLS Traffic Capture with tshark ===${NC}"
echo "Namespace: ${NAMESPACE}"
echo

# Find Envoy pod
ENVOY_POD=$(kubectl get pods -n "${NAMESPACE}" -l app=envoy-proxy -o jsonpath='{.items[0].metadata.name}')
if [ -z "${ENVOY_POD}" ]; then
    echo -e "${RED}Error: No Envoy pod found in namespace ${NAMESPACE}${NC}"
    exit 1
fi

echo "Envoy Pod: ${ENVOY_POD}"
echo

# Step 1: Extract private keys
echo -e "${YELLOW}[1/3] Extracting private keys...${NC}"

TMP_DIR=$(mktemp -d)
trap 'rm -rf "${TMP_DIR}"' EXIT

# Extract all private keys
echo "  - Extracting Envoy server key..."
kubectl get secret -n "${NAMESPACE}" envoy-server-cert-pem -o jsonpath='{.data.cert\.pem}' | \
    base64 -d > "${TMP_DIR}/envoy-server-full.pem"
openssl pkey -in "${TMP_DIR}/envoy-server-full.pem" -out "${TMP_DIR}/envoy-server.key" 2>/dev/null

echo "  - Extracting Envoy client key..."
kubectl get secret -n "${NAMESPACE}" envoy-client-cert-pem -o jsonpath='{.data.cert\.pem}' | \
    base64 -d > "${TMP_DIR}/envoy-client-full.pem"
openssl pkey -in "${TMP_DIR}/envoy-client-full.pem" -out "${TMP_DIR}/envoy-client.key" 2>/dev/null

echo "  - Extracting mongod key..."
kubectl get secret -n "${NAMESPACE}" certs-mdb-ent-tls-cert-pem -o jsonpath='{.data.tls\.pem}' 2>/dev/null | \
    base64 -d > "${TMP_DIR}/mongod-full.pem" || \
    kubectl get secret -n "${NAMESPACE}" certs-mdb-ent-tls-cert-pem -o jsonpath='{.data.cert\.pem}' | \
    base64 -d > "${TMP_DIR}/mongod-full.pem"
openssl pkey -in "${TMP_DIR}/mongod-full.pem" -out "${TMP_DIR}/mongod.key" 2>/dev/null

# Try to get mongot key
echo "  - Extracting mongot key..."
kubectl get secret -n "${NAMESPACE}" mdb-ent-tls-search-certificate-key -o jsonpath='{.data.certificate}' 2>/dev/null | \
    base64 -d > "${TMP_DIR}/mongot-full.pem" && \
    openssl pkey -in "${TMP_DIR}/mongot-full.pem" -out "${TMP_DIR}/mongot.key" 2>/dev/null || \
    echo "    (mongot key not found, will try without it)"

# Combine all keys
cat "${TMP_DIR}"/*.key > "${TMP_DIR}/all-keys.pem" 2>/dev/null

echo -e "${GREEN}  ✓ Keys extracted${NC}"
echo

# Step 2: Create a ConfigMap with the keys for the ephemeral container
echo -e "${YELLOW}[2/3] Creating temporary ConfigMap with keys...${NC}"

kubectl create configmap -n "${NAMESPACE}" tshark-keys --from-file="${TMP_DIR}/all-keys.pem" \
    --dry-run=client -o yaml | kubectl apply -f -

echo -e "${GREEN}  ✓ ConfigMap created${NC}"
echo

# Step 3: Create pod spec for the ephemeral container with tshark
echo -e "${YELLOW}[3/3] Starting ephemeral debug container with tshark...${NC}"
echo
echo "This will:"
echo "  1. Attach an ephemeral container to the Envoy pod"
echo "  2. Mount the private keys"
echo "  3. Run tshark with TLS decryption"
echo
echo -e "${YELLOW}Press Ctrl+C to stop capturing${NC}"
echo

# Create a temporary pod spec that includes volume mount for keys
cat > "${TMP_DIR}/debug-spec.yaml" <<EOF
{
  "ephemeralContainers": [{
    "name": "tshark-debug",
    "image": "nicolaka/netshoot",
    "command": ["/bin/bash"],
    "stdin": true,
    "tty": true,
    "securityContext": {
      "capabilities": {
        "add": ["NET_RAW", "NET_ADMIN"]
      }
    },
    "volumeMounts": [{
      "name": "tshark-keys",
      "mountPath": "/keys",
      "readOnly": true
    }]
  }]
}
EOF

# Note: We need to patch the pod to add the volume first
echo "Adding keys volume to pod..."
kubectl patch pod -n "${NAMESPACE}" "${ENVOY_POD}" --type=json -p '[
  {
    "op": "add",
    "path": "/spec/volumes/-",
    "value": {
      "name": "tshark-keys",
      "configMap": {
        "name": "tshark-keys"
      }
    }
  }
]' 2>/dev/null || echo "  (Volume may already exist or pod needs restart)"

echo
echo -e "${GREEN}Starting tshark in ephemeral container...${NC}"
echo

# Start the debug container with tshark
# The nicolaka/netshoot image has tshark pre-installed
kubectl debug -n "${NAMESPACE}" "${ENVOY_POD}" -it --image=nicolaka/netshoot \
    --target=envoy -- /bin/bash -c "
# Install additional tools if needed
# apk add --no-cache tshark 2>/dev/null || true

# Check if we have keys mounted
if [ ! -f /etc/envoy/tls/server/cert.pem ]; then
    echo 'Warning: Keys not accessible via volume mount'
    echo 'Extracting keys from pod...'
    mkdir -p /tmp/keys
    # Keys are already in the pod at /etc/envoy/tls/
    cat /etc/envoy/tls/server/cert.pem > /tmp/all-keys.pem 2>/dev/null || true
    cat /etc/envoy/tls/client/cert.pem >> /tmp/all-keys.pem 2>/dev/null || true
    KEYFILE=/tmp/all-keys.pem
else
    KEYFILE=/etc/envoy/tls/server/cert.pem
fi

echo '=== Starting tshark capture with TLS decryption ==='
echo 'Capturing on all interfaces, port 27028'
echo 'Press Ctrl+C to stop'
echo

# Run tshark with TLS decryption
# -i any: capture on all interfaces
# -f 'port 27028': filter for gRPC traffic
# -o tls.keys_list: specify RSA keys for decryption
# -Y http2: display filter for HTTP/2 (decrypted gRPC)
# -V: verbose output
# -w: write to file
tshark -i any -f 'port 27028 or port 9901' \
    -o tls.desegment_ssl_records:TRUE \
    -o tls.desegment_ssl_application_data:TRUE \
    -o \"tls.keys_list:0.0.0.0,27028,data,\$KEYFILE\" \
    -w /tmp/capture.pcap \
    -P \
    -V

echo
echo 'Capture stopped. Copying to host...'
"

echo
echo -e "${GREEN}Capture completed!${NC}"
echo

# Copy the capture file from the ephemeral container
DEBUG_CONTAINER=$(kubectl get pod -n "${NAMESPACE}" "${ENVOY_POD}" -o jsonpath='{.spec.ephemeralContainers[?(@.name=="tshark-debug")].name}')

if [ -n "${DEBUG_CONTAINER}" ]; then
    echo "Copying capture file..."
    kubectl cp "${NAMESPACE}/${ENVOY_POD}:/tmp/capture.pcap" "./${OUTPUT_FILE}" -c "${DEBUG_CONTAINER}"
    echo -e "${GREEN}Capture saved to: ${OUTPUT_FILE}${NC}"

    # Also save the keys for later analysis
    cp "${TMP_DIR}/all-keys.pem" "./all-keys-$(date +%Y%m%d-%H%M%S).pem"
    echo "Keys saved for Wireshark analysis"
fi

# Cleanup
echo
echo "Cleaning up temporary ConfigMap..."
kubectl delete configmap -n "${NAMESPACE}" tshark-keys 2>/dev/null || true

echo
echo -e "${GREEN}=== Done ===${NC}"
echo
echo "To analyze the capture:"
echo "  1. Open ${OUTPUT_FILE} in Wireshark"
echo "  2. Edit → Preferences → Protocols → TLS"
echo "  3. Add RSA keys list entry:"
echo "     IP: any, Port: 27028, Protocol: data, Key File: ./all-keys-*.pem"
echo "  4. Use display filter: http2 or grpc"