#!/bin/bash

# Wrapper to run Envoy debug commands from mongodb-tools-pod
#
# Usage:
#   ./kubectl-debug-envoy.sh                 # Quick stats
#   ./kubectl-debug-envoy.sh --watch         # Watch mode
#   ./kubectl-debug-envoy.sh --clusters      # Cluster info
#   ./kubectl-debug-envoy.sh --all           # Full dump

NAMESPACE="${NAMESPACE:-ls}"
CLUSTER_NAME="${CLUSTER_NAME:-}"
TOOLS_POD="${TOOLS_POD:-mongodb-tools-pod}"

# Check if tools pod exists
if ! kubectl get pod "${TOOLS_POD}" --context "${CLUSTER_NAME}" --namespace "${NAMESPACE}" &>/dev/null; then
    echo "ERROR: Pod '${TOOLS_POD}' not found in namespace '${NAMESPACE}'"
    echo "Make sure mongodb-tools-pod is deployed"
    exit 1
fi

# The debug script content (embedded)
DEBUG_SCRIPT='
ENVOY_ADMIN_HOST="${ENVOY_ADMIN_HOST:-envoy-proxy-admin.ls.svc.cluster.local}"
ENVOY_ADMIN_PORT="${ENVOY_ADMIN_PORT:-9901}"
ENVOY_URL="http://${ENVOY_ADMIN_HOST}:${ENVOY_ADMIN_PORT}"

RED="\033[0;31m"
GREEN="\033[0;32m"
YELLOW="\033[1;33m"
BLUE="\033[0;34m"
NC="\033[0m"

print_header() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}  $(date +%Y-%m-%d\ %H:%M:%S)${NC}"
    echo -e "${BLUE}========================================${NC}"
}

print_section() {
    echo ""
    echo -e "${YELLOW}--- $1 ---${NC}"
}

check_envoy() {
    if ! curl -s --connect-timeout 2 "${ENVOY_URL}/ready" > /dev/null 2>&1; then
        echo -e "${RED}ERROR: Cannot reach Envoy admin at ${ENVOY_URL}${NC}"
        exit 1
    fi
}

get_health() {
    print_section "Health Status"
    echo -n "Ready: "
    curl -s "${ENVOY_URL}/ready" && echo "" || echo -e "${RED}NOT READY${NC}"
}

get_stats_summary() {
    print_section "Quick Stats Summary"

    echo "Downstream (mongod -> Envoy):"
    downstream_active=$(curl -s "${ENVOY_URL}/stats?filter=downstream_cx_active" | grep "listener.*downstream_cx_active" | awk "{sum+=\$2} END {print sum}")
    downstream_total=$(curl -s "${ENVOY_URL}/stats?filter=downstream_cx_total" | grep "listener.*downstream_cx_total" | awk "{sum+=\$2} END {print sum}")
    echo "  Active connections: ${downstream_active:-0}"
    echo "  Total connections: ${downstream_total:-0}"

    echo ""
    echo "Upstream (Envoy -> mongot):"
    for cluster in mongot_rs1_cluster mongot_rs2_cluster; do
        active=$(curl -s "${ENVOY_URL}/stats?filter=cluster.${cluster}.upstream_cx_active" | awk "{print \$2}")
        total=$(curl -s "${ENVOY_URL}/stats?filter=cluster.${cluster}.upstream_cx_total" | awk "{print \$2}")
        echo "  ${cluster}: Active=${active:-0}, Total=${total:-0}"
    done

    echo ""
    echo "Requests:"
    rq_total=$(curl -s "${ENVOY_URL}/stats?filter=downstream_rq_total" | grep "http.*downstream_rq_total" | awk "{sum+=\$2} END {print sum}")
    rq_active=$(curl -s "${ENVOY_URL}/stats?filter=downstream_rq_active" | grep "http.*downstream_rq_active" | awk "{sum+=\$2} END {print sum}")
    echo "  Active: ${rq_active:-0}, Total: ${rq_total:-0}"
}

get_clusters() {
    print_section "Cluster Health"
    curl -s "${ENVOY_URL}/clusters" | grep -E "^[a-z].*::|priority|health_flags" | head -30

    print_section "Upstream Connection Stats"
    curl -s "${ENVOY_URL}/stats?filter=cluster.*upstream_cx" | grep -E "active|total|connect_fail" | head -20
}

get_connections() {
    print_section "Downstream Connections"
    curl -s "${ENVOY_URL}/stats?filter=downstream_cx" | grep -E "active|total|destroy" | head -15

    print_section "Upstream Connections"
    curl -s "${ENVOY_URL}/stats?filter=upstream_cx" | grep -E "active|total|connect_fail" | head -20
}

get_errors() {
    print_section "Errors (non-zero only)"
    curl -s "${ENVOY_URL}/stats?filter=error" | grep -v ": 0$" | head -20

    print_section "Timeouts (non-zero only)"
    curl -s "${ENVOY_URL}/stats?filter=timeout" | grep -v ": 0$" | head -10
}

get_tls() {
    print_section "TLS Stats"
    curl -s "${ENVOY_URL}/stats?filter=ssl" | grep -E "handshake|fail|session" | head -20
}

get_listeners() {
    print_section "Listeners"
    curl -s "${ENVOY_URL}/listeners" | head -20
}

watch_stats() {
    interval="${1:-5}"
    echo -e "${GREEN}Watching every ${interval}s. Ctrl+C to stop.${NC}"
    while true; do
        clear
        print_header "Envoy Stats Monitor"
        get_stats_summary
        get_errors
        echo ""
        echo -e "${YELLOW}Refreshing in ${interval}s...${NC}"
        sleep "${interval}"
    done
}

dump_all() {
    print_header "Full Envoy Debug Dump"
    get_health
    get_stats_summary
    get_clusters
    get_connections
    get_tls
    get_errors
}

check_envoy

case "${1:-}" in
    --watch|-w) watch_stats "${2:-5}" ;;
    --clusters|-c) print_header "Clusters"; get_clusters ;;
    --connections|-n) print_header "Connections"; get_connections ;;
    --errors|-e) print_header "Errors"; get_errors ;;
    --tls|-t) print_header "TLS"; get_tls ;;
    --listeners|-l) print_header "Listeners"; get_listeners ;;
    --all|-a) dump_all ;;
    --help|-h)
        echo "Usage: [option]"
        echo "  (none)      Quick stats"
        echo "  --watch,-w  Watch mode"
        echo "  --clusters  Cluster info"
        echo "  --connections Connection stats"
        echo "  --errors    Error stats"
        echo "  --tls       TLS stats"
        echo "  --listeners Listener info"
        echo "  --all       Full dump"
        ;;
    *)
        print_header "Envoy Quick Stats"
        get_health
        get_stats_summary
        ;;
esac
'

# Run the debug script in the tools pod
echo "Running Envoy debug in ${TOOLS_POD}..."
echo ""

kubectl exec --context "${CLUSTER_NAME}" --namespace "${NAMESPACE}" "${TOOLS_POD}" -- bash -c "${DEBUG_SCRIPT}" -- "$@"
