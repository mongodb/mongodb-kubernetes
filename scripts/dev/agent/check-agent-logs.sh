#!/bin/bash

source /Users/nam.nguyen/projects/ops-manager-kubernetes/.generated/context.export.env

# Check if NAMESPACE is set
if [ -z "$NAMESPACE" ]; then
    echo "Error: NAMESPACE environment variable is not set"
    exit 1
fi

echo "Checking agent logs for Kubernetes moves in namespace: $NAMESPACE"
echo "=================================================================="

# Get all StatefulSets with owner mongodb.com/v1
STS_LIST=$(kubectl get sts -n "$NAMESPACE" -o jsonpath='{range .items[?(@.metadata.ownerReferences[0].apiVersion=="mongodb.com/v1")]}{.metadata.name}{"\n"}{end}')

if [ -z "$STS_LIST" ]; then
    echo "No StatefulSets found with owner mongodb.com/v1 in namespace $NAMESPACE"
    exit 0
fi

PODS_FOUND=()

for sts in $STS_LIST; do
    echo "Processing StatefulSet: $sts"

    # Get pods for this StatefulSet
    PODS=$(kubectl get pods -n "$NAMESPACE" -l app="${sts}-svc" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)

    if [ -z "$PODS" ]; then
        PODS=$(kubectl get pods -n "$NAMESPACE" -o json | jq -r '.items[] | select(.metadata.ownerReferences[]?.name=="'$sts'") | .metadata.name' 2>/dev/null)
    fi

    if [ -z "$PODS" ]; then
        echo "  No pods found for StatefulSet $sts"
        continue
    fi

    for pod in $PODS; do
        echo "  Processing pod: $pod"
        PODS_FOUND+=("$pod")
    done
done

if [ ${#PODS_FOUND[@]} -eq 0 ]; then
    echo "No pods found"
    exit 0
fi

echo ""
echo "🔍 Analyzing Agent Logs for Kubernetes-related Moves"
echo "====================================================="

for pod in "${PODS_FOUND[@]}"; do
    echo ""
    echo "📋 Pod: $pod"
    echo "$(printf '%.0s─' {1..60})"

    # Check if log file exists
    if ! kubectl exec -n "$NAMESPACE" "$pod" -- test -f /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null; then
        echo "❌ Log file not found: /var/log/mongodb-mms-automation/automation-agent-verbose.log"
        continue
    fi

    echo "🔄 Recent Kubernetes/Move-related log entries:"
    echo ""

    # Search for Kubernetes-related moves and recent activity
    kubectl exec -n "$NAMESPACE" "$pod" -- tail -n 500 /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null | \
    grep -E "(move|Move|MOVE|kubernetes|k8s|wait|Wait|WAIT|step|Step|STEP|goal|Goal|GOAL|plan|Plan|PLAN)" | \
    tail -n 20 || echo "No recent move-related entries found"

    echo ""
    echo "🎯 Current Wait Steps:"
    kubectl exec -n "$NAMESPACE" "$pod" -- tail -n 200 /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null | \
    grep -E "(WaitAllRsMembersUp|WaitPrimary|WaitRsInit|WaitProcessUp|wait.*step)" | \
    tail -n 10 || echo "No current wait steps found"

    echo ""
    echo "⚠️  Recent Errors/Warnings:"
    kubectl exec -n "$NAMESPACE" "$pod" -- tail -n 300 /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null | \
    grep -iE "(error|Error|ERROR|warn|Warn|WARN|fail|Fail|FAIL)" | \
    tail -n 5 || echo "No recent errors/warnings found"

    echo ""
    echo "📈 Recent Goal/Plan Activity:"
    kubectl exec -n "$NAMESPACE" "$pod" -- tail -n 200 /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null | \
    grep -E "(goal.*version|plan.*start|plan.*complet|automation.*config)" | \
    tail -n 5 || echo "No recent goal/plan activity found"

    echo ""
    echo "🔗 Replica Set Status:"
    kubectl exec -n "$NAMESPACE" "$pod" -- tail -n 200 /var/log/mongodb-mms-automation/automation-agent-verbose.log 2>/dev/null | \
    grep -E "(replica.*set|rs.*init|primary|secondary|replication)" | \
    tail -n 5 || echo "No recent replica set activity found"

    echo "$(printf '%.0s═' {1..60})"
done

echo ""
echo "💡 Log Analysis Summary"
echo "======================"
echo "Analyzed logs from ${#PODS_FOUND[@]} pods for:"
echo "  • Move/Step execution status"
echo "  • Wait conditions and blocking steps"
echo "  • Error conditions and warnings"
echo "  • Goal/Plan progression"
echo "  • Replica set initialization status"
echo ""
echo "✅ Analysis complete!"