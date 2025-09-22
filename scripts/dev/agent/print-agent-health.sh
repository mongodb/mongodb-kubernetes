#!/bin/bash

source /Users/nam.nguyen/projects/ops-manager-kubernetes/.generated/context.export.env

# Check if NAMESPACE is set
if [ -z "$NAMESPACE" ]; then
    echo "Error: NAMESPACE environment variable is not set"
    exit 1
fi

# Create temporary directory for health status files
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "Fetching agent health status from pods in namespace: $NAMESPACE"
echo "================================================================"

# Get all StatefulSets with owner mongodb.com/v1
STS_LIST=$(kubectl get sts -n "$NAMESPACE" -o jsonpath='{range .items[?(@.metadata.ownerReferences[0].apiVersion=="mongodb.com/v1")]}{.metadata.name}{"\n"}{end}')

if [ -z "$STS_LIST" ]; then
    echo "No StatefulSets found with owner mongodb.com/v1 in namespace $NAMESPACE"
    exit 0
fi

# Collect all health status data
PODS_FOUND=()
PODS_WITH_DATA=()
PODS_WITHOUT_DATA=()

for sts in $STS_LIST; do
    echo "Processing StatefulSet: $sts"

    # Get pods for this StatefulSet using correct label selector
    PODS=$(kubectl get pods -n "$NAMESPACE" -l app="${sts}-svc" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null)

    if [ -z "$PODS" ]; then
        # Try alternative selector patterns
        PODS=$(kubectl get pods -n "$NAMESPACE" -o json | jq -r '.items[] | select(.metadata.ownerReferences[]?.name=="'$sts'") | .metadata.name' 2>/dev/null)
    fi

    if [ -z "$PODS" ]; then
        echo "  No pods found for StatefulSet $sts"
        continue
    fi

    for pod in $PODS; do
        echo "  Processing pod: $pod"
        PODS_FOUND+=("$pod")

        # Get agent health status
        if kubectl exec -n "$NAMESPACE" "$pod" -- test -f /var/log/mongodb-mms-automation/agent-health-status.json 2>/dev/null; then
            if kubectl exec -n "$NAMESPACE" "$pod" -- cat /var/log/mongodb-mms-automation/agent-health-status.json 2>/dev/null > "$TEMP_DIR/${pod}.json"; then
                # Check if file has content
                if [ -s "$TEMP_DIR/${pod}.json" ]; then
                    PODS_WITH_DATA+=("$pod")
                else
                    echo "    File exists but is empty"
                    PODS_WITHOUT_DATA+=("$pod")
                fi
            else
                echo "    Could not read file"
                PODS_WITHOUT_DATA+=("$pod")
            fi
        else
            echo "    File not found: /var/log/mongodb-mms-automation/agent-health-status.json"
            PODS_WITHOUT_DATA+=("$pod")
        fi
    done
done

echo ""
echo "Agent Health Status Summary"
echo "=========================="

if [ ${#PODS_FOUND[@]} -eq 0 ]; then
    echo "No pods found with agent health status files"
    exit 0
fi

# Pretty print individual status files
for pod in "${PODS_WITH_DATA[@]}"; do
    echo ""
    echo "🔍 Pod: $pod"
    echo "$(printf '%.0s─' {1..50})"

    if command -v jq &> /dev/null; then
        jq -C . "$TEMP_DIR/${pod}.json" 2>/dev/null || cat "$TEMP_DIR/${pod}.json"
    else
        cat "$TEMP_DIR/${pod}.json"
    fi
done

# Show pods without data
if [ ${#PODS_WITHOUT_DATA[@]} -gt 0 ]; then
    echo ""
    echo "❌ Pods without health data:"
    for pod in "${PODS_WITHOUT_DATA[@]}"; do
        echo "  - $pod"
    done
fi

# Show differences if multiple pods with data exist
if [ ${#PODS_WITH_DATA[@]} -gt 1 ]; then
    echo ""
    echo ""
    echo "🔄 Health Status Comparison"
    echo "=========================="

    # Create a combined comparison view
    if command -v jq &> /dev/null; then
        echo "Key Status Comparison:"
        echo "$(printf '%.0s─' {1..80})"

        # Extract key fields for comparison
        for pod in "${PODS_WITH_DATA[@]}"; do
            echo -n "Pod $pod: "
            # Try to extract overall state or first process state
            STATE=$(jq -r '.state // (.statuses | keys[0] as $k | .[$k].IsInGoalState) // "unknown"' "$TEMP_DIR/${pod}.json" 2>/dev/null)
            if [ "$STATE" = "true" ]; then
                echo "✅ In Goal State"
            elif [ "$STATE" = "false" ]; then
                echo "❌ Not in Goal State"
            else
                echo "$STATE"
            fi
        done

        echo ""
        echo "Process Details:"
        echo "$(printf '%.0s─' {1..80})"

        for pod in "${PODS_WITH_DATA[@]}"; do
            echo "Pod $pod:"
            # Extract process information
            jq -r '
                if .statuses then
                    .statuses | to_entries[] | "  \(.key): IsInGoalState=\(.value.IsInGoalState), ReplicationStatus=\(.value.ReplicationStatus // "N/A")"
                elif .processes then
                    .processes[] | "  \(.name): \(.state // "unknown")"
                else
                    "  No process information found"
                end
            ' "$TEMP_DIR/${pod}.json" 2>/dev/null || echo "  Parse error"
            echo ""
        done

        echo "MMS Status Summary:"
        echo "$(printf '%.0s─' {1..80})"

        for pod in "${PODS_WITH_DATA[@]}"; do
            echo "Pod $pod:"
            jq -r '
                if .mmsStatus then
                    .mmsStatus | to_entries[] | "  \(.key): GoalVersion=\(.value.lastGoalVersionAchieved // "N/A"), Responsive=\(.value.responsive // "N/A")"
                else
                    "  No MMS status found"
                end
            ' "$TEMP_DIR/${pod}.json" 2>/dev/null || echo "  Parse error"
            echo ""
        done
    fi

    # Show file differences using diff if exactly 2 pods with data
    if [ ${#PODS_WITH_DATA[@]} -eq 2 ] && command -v diff &> /dev/null; then
        pod1="${PODS_WITH_DATA[0]}"
        pod2="${PODS_WITH_DATA[1]}"

        echo ""
        echo "📊 Detailed Diff between $pod1 and $pod2:"
        echo "$(printf '%.0s─' {1..80})"

        if command -v jq &> /dev/null; then
            # Pretty print both files for comparison
            jq . "$TEMP_DIR/${pod1}.json" > "$TEMP_DIR/${pod1}_pretty.json" 2>/dev/null
            jq . "$TEMP_DIR/${pod2}.json" > "$TEMP_DIR/${pod2}_pretty.json" 2>/dev/null
            diff -u "$TEMP_DIR/${pod1}_pretty.json" "$TEMP_DIR/${pod2}_pretty.json" || true
        else
            diff -u "$TEMP_DIR/${pod1}.json" "$TEMP_DIR/${pod2}.json" || true
        fi
    fi
fi

echo ""
echo "✅ Health status collection complete!"
echo "   Found ${#PODS_FOUND[@]} total pods"
echo "   ${#PODS_WITH_DATA[@]} pods with health data"
echo "   ${#PODS_WITHOUT_DATA[@]} pods without health data"