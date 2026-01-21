#!/bin/bash
# Simplified TLS traffic capture using tcpdump
# This captures raw packets and decrypts them locally with Wireshark

set -euo pipefail

NAMESPACE="${1:-ls}"
OUTPUT_FILE="${2:-envoy-capture-$(date +%Y%m%d-%H%M%S)}"

echo "=== TLS Traffic Capture with tcpdump ==="
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
echo "Extracting TLS keys for decryption..."
mkdir -p "./captures"

kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/server/cert.pem > "./captures/${OUTPUT_FILE}-keys.pem" 2>/dev/null
kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/client/cert.pem >> "./captures/${OUTPUT_FILE}-keys.pem" 2>/dev/null

echo "Keys saved to: ./captures/${OUTPUT_FILE}-keys.pem"
echo

echo "Starting ephemeral debug container with tcpdump..."
echo "This will capture traffic on port 27028 (gRPC) and 9901 (admin)"
echo
echo "Press Ctrl+C to stop capturing"
echo
echo "Note: The container runs in the background. When you stop it,"
echo "we'll copy the capture file automatically."
echo

# Start debug container with tcpdump
# NOT using --target to avoid inheriting non-root security context
# shellcheck disable=SC2016
kubectl debug -n "${NAMESPACE}" "${ENVOY_POD}" -it --image=nicolaka/netshoot -- \
    sh -c '
echo "=== Starting tcpdump ==="
echo "Running as: $(id)"
echo

# Run tcpdump to capture packets (trap SIGINT to handle graceful shutdown)
trap "echo \"Received stop signal\"" INT TERM

tcpdump -i any -s 0 \
    -w /tmp/capture.pcap \
    "port 27028 or port 9901"

echo
echo "Capture stopped"
ls -lh /tmp/capture.pcap
echo
echo "Keeping container alive for 30 seconds to allow file copy..."
echo "Do not press Ctrl+C again, the script will copy the file automatically."
sleep 30
'

echo
echo "Capture stopped. Copying file..."

# Find the debug container name
DEBUG_CONTAINER=$(kubectl get pod -n "${NAMESPACE}" "${ENVOY_POD}" -o jsonpath='{.spec.ephemeralContainers[-1].name}')
echo "Debug container: ${DEBUG_CONTAINER}"

if [ -n "${DEBUG_CONTAINER}" ]; then
    # Give it a moment to finish writing
    sleep 1

    echo "Copying capture file..."

    # Try kubectl cp first
    if kubectl cp "${NAMESPACE}/${ENVOY_POD}:/tmp/capture.pcap" "./captures/${OUTPUT_FILE}.pcap" -c "${DEBUG_CONTAINER}" 2>/dev/null; then
        echo "Successfully copied capture file"
    else
        echo "kubectl cp failed, trying exec method..."
        # Alternative: use kubectl exec to cat the file
        if kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -c "${DEBUG_CONTAINER}" -- cat /tmp/capture.pcap > "./captures/${OUTPUT_FILE}.pcap" 2>/dev/null; then
            echo "Successfully copied via exec"
        else
            echo "Warning: Could not copy file automatically"
            echo "Manual copy command:"
            echo "  kubectl exec -n ${NAMESPACE} ${ENVOY_POD} -c ${DEBUG_CONTAINER} -- cat /tmp/capture.pcap > ./captures/${OUTPUT_FILE}.pcap"
        fi
    fi

    if [ -f "./captures/${OUTPUT_FILE}.pcap" ] && [ -s "./captures/${OUTPUT_FILE}.pcap" ]; then
        FILESIZE=$(du -h "./captures/${OUTPUT_FILE}.pcap" | cut -f1)
        echo
        echo "=== Capture Complete ==="
        echo "Capture file: ./captures/${OUTPUT_FILE}.pcap (${FILESIZE})"
        echo "Keys file: ./captures/${OUTPUT_FILE}-keys.pem"
        echo

        # Count packets
        PACKET_COUNT=$(tcpdump -r "./captures/${OUTPUT_FILE}.pcap" 2>/dev/null | wc -l || echo "N/A")
        echo "Packets captured: ${PACKET_COUNT}"
        echo

        echo "=== To analyze in Wireshark ==="
        echo "1. Open ./captures/${OUTPUT_FILE}.pcap"
        echo "2. Edit → Preferences → Protocols → TLS"
        echo "3. RSA keys list → Edit → Add:"
        echo "     IP: any"
        echo "     Port: 27028"
        echo "     Protocol: data"
        echo "     Key File: $(pwd)/captures/${OUTPUT_FILE}-keys.pem"
        echo "4. Click OK"
        echo "5. Use display filter: http2 or grpc"
        echo

        echo "=== To view decrypted traffic in terminal ==="
        echo "tshark -r ./captures/${OUTPUT_FILE}.pcap \\"
        echo "  -o \"tls.keys_list:0.0.0.0,27028,data,./captures/${OUTPUT_FILE}-keys.pem\" \\"
        echo "  -Y http2 -V"
    else
        echo "Error: Capture file not found or empty"
    fi
else
    echo "Error: Could not find debug container"
fi