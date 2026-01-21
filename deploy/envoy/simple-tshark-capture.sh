#!/bin/bash
# Simple script to capture TLS traffic using tshark in an ephemeral container
# Keys are already mounted in the Envoy pod, so we can reuse them

set -euo pipefail

NAMESPACE="${1:-ls}"
OUTPUT_FILE="${2:-envoy-capture-$(date +%Y%m%d-%H%M%S)}"

echo "=== TLS Traffic Capture with tshark ==="
echo "Namespace: ${NAMESPACE}"
echo "Output: ${OUTPUT_FILE}.pcap"
echo

# Find Envoy pod
ENVOY_POD=$(kubectl get pods -n "${NAMESPACE}" -l app=envoy-proxy -o jsonpath='{.items[0].metadata.name}')
if [ -z "${ENVOY_POD}" ]; then
    echo "Error: No Envoy pod found in namespace ${NAMESPACE}"
    exit 1
fi

echo "Envoy Pod: ${ENVOY_POD}"
echo

# Extract keys locally for later Wireshark analysis
echo "Extracting keys for later analysis..."
mkdir -p "./captures"

kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/server/cert.pem > "./captures/${OUTPUT_FILE}-keys.pem" 2>/dev/null
kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/client/cert.pem >> "./captures/${OUTPUT_FILE}-keys.pem" 2>/dev/null

echo "Keys saved to: ./captures/${OUTPUT_FILE}-keys.pem"
echo

echo "Starting ephemeral debug container with tshark..."
echo "This will capture traffic on port 27028 (gRPC) and 9901 (admin)"
echo
echo "Press Ctrl+C to stop capturing"
echo

# Use kubectl debug to attach ephemeral container
# The container shares the network namespace with Envoy
# We need to run as root (user 0) to use tcpdump/tshark with raw sockets
kubectl debug -n "${NAMESPACE}" "${ENVOY_POD}" -it --image=nicolaka/netshoot \
    --target=envoy \
    --profile=netadmin \
    --custom='{"spec":{"containers":[{"name":"debugger","image":"nicolaka/netshoot","command":["/bin/bash"],"stdin":true,"tty":true,"securityContext":{"runAsUser":0,"runAsNonRoot":false,"capabilities":{"add":["NET_RAW","NET_ADMIN"]}}}]}}' \
    -- bash -c '
set -x

echo "=== tshark TLS capture starting ==="
echo

# Extract keys from the mounted volumes (shared with Envoy container)
mkdir -p /tmp/keys
cat /etc/envoy/tls/server/cert.pem > /tmp/all-keys.pem 2>/dev/null || true
cat /etc/envoy/tls/client/cert.pem >> /tmp/all-keys.pem 2>/dev/null || true

echo "Keys extracted from Envoy volumes"
ls -l /tmp/all-keys.pem

echo
echo "Starting tshark capture..."
echo "Interfaces available:"
ip link show

echo
echo "Capturing on all interfaces, filtering port 27028 and 9901"
echo

# Run tshark with TLS decryption
tshark -i any \
    -f "port 27028 or port 9901" \
    -o tls.desegment_ssl_records:TRUE \
    -o tls.desegment_ssl_application_data:TRUE \
    -o "tls.keys_list:0.0.0.0,27028,data,/tmp/all-keys.pem" \
    -w /tmp/capture.pcap \
    -P

echo
echo "Capture stopped"
ls -lh /tmp/capture.pcap
'

echo
echo "Copying capture file from pod..."

# Find the debug container name
DEBUG_CONTAINER=$(kubectl get pod -n "${NAMESPACE}" "${ENVOY_POD}" -o jsonpath='{.spec.ephemeralContainers[-1].name}')

if [ -n "${DEBUG_CONTAINER}" ]; then
    kubectl cp "${NAMESPACE}/${ENVOY_POD}:/tmp/capture.pcap" "./captures/${OUTPUT_FILE}.pcap" -c "${DEBUG_CONTAINER}" || \
        echo "Warning: Could not copy capture file. You may need to copy it manually."

    echo
    echo "=== Capture Complete ==="
    echo "Capture file: ./captures/${OUTPUT_FILE}.pcap"
    echo "Keys file: ./captures/${OUTPUT_FILE}-keys.pem"
    echo
    echo "To analyze in Wireshark:"
    echo "  1. Open ./captures/${OUTPUT_FILE}.pcap"
    echo "  2. Edit → Preferences → Protocols → TLS"
    echo "  3. RSA keys list → Edit → Add:"
    echo "       IP: any"
    echo "       Port: 27028"
    echo "       Protocol: data"
    echo "       Key File: $(pwd)/captures/${OUTPUT_FILE}-keys.pem"
    echo "  4. Use filter: http2 or grpc"
else
    echo "Could not find debug container to copy files"
fi