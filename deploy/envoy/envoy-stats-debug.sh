#!/bin/bash
#
# Envoy Stats Debugging Script
# Runs periodic curl requests to collect Envoy admin stats via mongodb-tools-pod
#
# Usage:
#   ./envoy-stats-debug.sh [OPTIONS]
#
# Options:
#   -n, --namespace     Kubernetes namespace (default: ls)
#   -p, --pod           Tools pod name (default: mongodb-tools-pod)
#   -e, --envoy-svc     Envoy service name (default: envoy-proxy-svc)
#   -i, --interval      Interval between stat collections in seconds (default: 10)
#   -d, --duration      Total duration to run in seconds (default: 300, 0 for infinite)
#   -o, --output-dir    Output directory for stats files (default: ./envoy-stats)
#   -f, --filter        Filter stats by prefix (e.g., "cluster", "http", "listener")
#   -v, --verbose       Enable verbose output
#   -h, --help          Show this help message
#

set -euo pipefail

# Default configuration
NAMESPACE="${NAMESPACE:-ls}"
TOOLS_POD="${TOOLS_POD:-mongodb-tools-pod}"
ENVOY_SERVICE="${ENVOY_SERVICE:-envoy-proxy-svc}"
ENVOY_ADMIN_PORT="${ENVOY_ADMIN_PORT:-9901}"
INTERVAL="${INTERVAL:-10}"
DURATION="${DURATION:-300}"
OUTPUT_DIR="${OUTPUT_DIR:-./envoy-stats}"
FILTER="${FILTER:-}"
VERBOSE="${VERBOSE:-false}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--namespace)
            NAMESPACE="$2"
            shift 2
            ;;
        -p|--pod)
            TOOLS_POD="$2"
            shift 2
            ;;
        -e|--envoy-svc)
            ENVOY_SERVICE="$2"
            shift 2
            ;;
        -i|--interval)
            INTERVAL="$2"
            shift 2
            ;;
        -d|--duration)
            DURATION="$2"
            shift 2
            ;;
        -o|--output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -f|--filter)
            FILTER="$2"
            shift 2
            ;;
        -v|--verbose)
            VERBOSE="true"
            shift
            ;;
        -h|--help)
            head -30 "$0" | tail -25
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Envoy admin URL (accessible from within the cluster)
ENVOY_ADMIN_URL="http://${ENVOY_SERVICE}.${NAMESPACE}.svc.cluster.local:${ENVOY_ADMIN_PORT}"

log() {
    echo -e "${BLUE}[$(date '+%Y-%m-%d %H:%M:%S')]${NC} $1"
}

log_verbose() {
    if [[ "$VERBOSE" == "true" ]]; then
        echo -e "${YELLOW}[DEBUG]${NC} $1"
    fi
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

log_success() {
    echo -e "${GREEN}[OK]${NC} $1"
}

# Create output directory
mkdir -p "$OUTPUT_DIR"
log "Output directory: $OUTPUT_DIR"

# Check if tools pod is running
check_tools_pod() {
    log "Checking if $TOOLS_POD is running in namespace $NAMESPACE..."
    if ! kubectl get pod "$TOOLS_POD" -n "$NAMESPACE" &>/dev/null; then
        log_error "Pod $TOOLS_POD not found in namespace $NAMESPACE"
        log "Creating mongodb-tools-pod..."
        kubectl run "$TOOLS_POD" \
            --namespace="$NAMESPACE" \
            --image=mongodb/mongodb-community-server:8.0-ubi8 \
            --restart=Never \
            --command -- sleep infinity
        log "Waiting for pod to be ready..."
        kubectl wait --for=condition=Ready pod/"$TOOLS_POD" -n "$NAMESPACE" --timeout=60s
    fi
    log_success "Tools pod is ready"
}

# Execute curl command in tools pod
exec_curl() {
    local endpoint="$1"
    local description="$2"

    log_verbose "Fetching: $endpoint"
    kubectl exec "$TOOLS_POD" -n "$NAMESPACE" -- \
        curl -sf --max-time 10 "${ENVOY_ADMIN_URL}${endpoint}" 2>/dev/null || {
        log_error "Failed to fetch $description"
        return 1
    }
}

# Collect basic server info
collect_server_info() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/server_info_${timestamp}.txt"

    {
        echo "=== Envoy Server Info ==="
        echo "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        echo "--- Server Info ---"
        exec_curl "/server_info" "server info" || echo "N/A"
        echo ""
        echo "--- Ready Status ---"
        exec_curl "/ready" "ready status" || echo "N/A"
    } > "$output_file"

    log_verbose "Saved server info to $output_file"
}

# Collect all stats
collect_stats() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/stats_${timestamp}.txt"

    if [[ -n "$FILTER" ]]; then
        exec_curl "/stats?filter=${FILTER}" "filtered stats" > "$output_file" || return 1
    else
        exec_curl "/stats" "all stats" > "$output_file" || return 1
    fi

    log "Collected stats -> $output_file"
}

# Collect stats in JSON format
collect_stats_json() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/stats_${timestamp}.json"

    if [[ -n "$FILTER" ]]; then
        exec_curl "/stats?filter=${FILTER}&format=json" "filtered JSON stats" > "$output_file" || return 1
    else
        exec_curl "/stats?format=json" "JSON stats" > "$output_file" || return 1
    fi

    log_verbose "Collected JSON stats -> $output_file"
}

# Collect Prometheus format stats
collect_prometheus_stats() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/prometheus_${timestamp}.txt"

    exec_curl "/stats/prometheus" "Prometheus stats" > "$output_file" || return 1

    log_verbose "Collected Prometheus stats -> $output_file"
}

# Collect cluster stats
collect_cluster_stats() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/clusters_${timestamp}.txt"

    {
        echo "=== Cluster Stats ==="
        echo "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        exec_curl "/clusters" "cluster info" || echo "N/A"
    } > "$output_file"

    log_verbose "Collected cluster stats -> $output_file"
}

# Collect listener stats
collect_listener_stats() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/listeners_${timestamp}.txt"

    {
        echo "=== Listener Stats ==="
        echo "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        exec_curl "/listeners" "listener info" || echo "N/A"
    } > "$output_file"

    log_verbose "Collected listener stats -> $output_file"
}

# Collect config dump
collect_config_dump() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/config_dump_${timestamp}.json"

    exec_curl "/config_dump" "config dump" > "$output_file" || return 1

    log_verbose "Collected config dump -> $output_file"
}

# Collect certificates info
collect_certs_info() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/certs_${timestamp}.txt"

    {
        echo "=== Certificate Info ==="
        echo "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        exec_curl "/certs" "certificate info" || echo "N/A"
    } > "$output_file"

    log_verbose "Collected certificate info -> $output_file"
}

# Collect memory stats
collect_memory_stats() {
    local timestamp="$1"
    local output_file="${OUTPUT_DIR}/memory_${timestamp}.txt"

    {
        echo "=== Memory Stats ==="
        echo "Timestamp: $(date '+%Y-%m-%d %H:%M:%S')"
        echo ""
        exec_curl "/memory" "memory stats" || echo "N/A"
    } > "$output_file"

    log_verbose "Collected memory stats -> $output_file"
}

# Print summary stats
print_summary() {
    local timestamp="$1"

    log "--- Quick Stats Summary ---"

    # Get key connection stats
    local stats
    stats=$(exec_curl "/stats?filter=downstream_cx" "connection stats" 2>/dev/null || echo "")

    if [[ -n "$stats" ]]; then
        echo "$stats" | grep -E "(downstream_cx_total|downstream_cx_active|downstream_cx_destroy)" | head -10
    fi

    # Get upstream stats
    stats=$(exec_curl "/stats?filter=upstream_cx" "upstream stats" 2>/dev/null || echo "")

    if [[ -n "$stats" ]]; then
        echo "$stats" | grep -E "(upstream_cx_total|upstream_cx_active|upstream_cx_connect_fail)" | head -10
    fi

    # Get HTTP/gRPC stats
    stats=$(exec_curl "/stats?filter=http.ingress" "HTTP stats" 2>/dev/null || echo "")

    if [[ -n "$stats" ]]; then
        echo "$stats" | grep -E "(downstream_rq_total|downstream_rq_active|downstream_rq_2xx|downstream_rq_5xx)" | head -10
    fi
}

# Main collection loop
run_collection() {
    local start_time
    start_time=$(date +%s)
    local iteration=0

    log "Starting Envoy stats collection"
    log "  Namespace: $NAMESPACE"
    log "  Tools pod: $TOOLS_POD"
    log "  Envoy service: $ENVOY_SERVICE"
    log "  Interval: ${INTERVAL}s"
    log "  Duration: ${DURATION}s (0 = infinite)"
    log "  Filter: ${FILTER:-<none>}"
    log ""

    # Initial config dump (only once)
    local timestamp
    timestamp=$(date '+%Y%m%d_%H%M%S')
    collect_server_info "$timestamp"
    collect_config_dump "$timestamp"
    collect_certs_info "$timestamp"

    while true; do
        iteration=$((iteration + 1))
        timestamp=$(date '+%Y%m%d_%H%M%S')

        log "=== Collection #${iteration} at ${timestamp} ==="

        # Collect stats
        collect_stats "$timestamp"
        collect_stats_json "$timestamp"
        collect_cluster_stats "$timestamp"
        collect_listener_stats "$timestamp"
        collect_memory_stats "$timestamp"

        # Print summary if verbose
        if [[ "$VERBOSE" == "true" ]]; then
            print_summary "$timestamp"
        fi

        # Check if duration exceeded
        if [[ "$DURATION" -gt 0 ]]; then
            local elapsed=$(($(date +%s) - start_time))
            if [[ $elapsed -ge $DURATION ]]; then
                log "Duration of ${DURATION}s reached. Stopping."
                break
            fi
            log_verbose "Elapsed: ${elapsed}s / ${DURATION}s"
        fi

        # Wait for next iteration
        log_verbose "Sleeping for ${INTERVAL}s..."
        sleep "$INTERVAL"
    done

    log_success "Collection complete. Stats saved to: $OUTPUT_DIR"
}

# Cleanup function
cleanup() {
    log "Interrupted. Cleaning up..."
    exit 0
}

trap cleanup SIGINT SIGTERM

# Main
check_tools_pod
run_collection
