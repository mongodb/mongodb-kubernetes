#!/bin/bash
# Start an interactive debug container for traffic capture
# Run tcpdump commands manually inside the container

set -euo pipefail

NAMESPACE="${1:-ls}"

echo "=== Starting Interactive Debug Container ==="
echo "Namespace: ${NAMESPACE}"
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

TIMESTAMP=$(date +%Y%m%d-%H%M%S)
kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/server/cert.pem > "./captures/envoy-keys-${TIMESTAMP}.pem" 2>/dev/null
kubectl exec -n "${NAMESPACE}" "${ENVOY_POD}" -- cat /etc/envoy/tls/client/cert.pem >> "./captures/envoy-keys-${TIMESTAMP}.pem" 2>/dev/null

echo "Keys saved to: ./captures/envoy-keys-${TIMESTAMP}.pem"
echo

echo "============================================"
echo "INSTRUCTIONS:"
echo "============================================"
echo "1. You will be dropped into an interactive shell"
echo "2. Run this command to start capturing:"
echo
echo "   tcpdump -i any -s 0 -w /tmp/capture.pcap 'port 27028 or port 9901'"
echo
echo "3. Generate traffic (from another terminal)"
echo "4. Press Ctrl+C to stop tcpdump"
echo "5. From ANOTHER terminal, run:"
echo
echo "   ./copy-capture.sh ${NAMESPACE}"
echo
echo "6. Exit the debug container with: exit"
echo "============================================"
echo
echo "Starting debug container..."
sleep 2

# Start interactive debug container
kubectl debug -n "${NAMESPACE}" "${ENVOY_POD}" -it --image=nicolaka/netshoot -- bash

echo
echo "Debug session ended"