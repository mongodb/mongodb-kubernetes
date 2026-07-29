#!/usr/bin/env bash
set -euo pipefail

# Periodic GCP garbage collector for MongoDB Kubernetes code-snippet CI (KUBE-268).
#
# Deletes GKE clusters, DNS managed zones, and global load-balancer resources
# that are older than AGE_THRESHOLD_HOURS and match the code-snippet naming
# patterns. Reclaims resources leaked by failed or interrupted CI runs without
# touching resources from in-flight runs.
#
# Env:
#   AGE_THRESHOLD_HOURS  Hours of age after which a resource is eligible (default: 24).
#   MDB_GKE_PROJECT      GCP project ID (required).

AGE_THRESHOLD_HOURS="${AGE_THRESHOLD_HOURS:-24}"
: "${MDB_GKE_PROJECT:?MDB_GKE_PROJECT is required}"

threshold_seconds=$(( $(date -u +%s) - AGE_THRESHOLD_HOURS * 3600 ))
overall_failed=0

# Convert an RFC3339 timestamp to epoch seconds. Returns 0 on parse failure
# (is_stale treats 0 as "not stale" so unparseable resources are skipped).
ts_to_epoch() {
    local ts="$1"
    local epoch
    epoch=$(date -u -d "${ts}" +%s 2>/dev/null || true)
    if [[ -z "${epoch}" || ! "${epoch}" =~ ^[0-9]+$ ]]; then
        echo 0
    else
        echo "${epoch}"
    fi
}

# Return 0 if the creation timestamp is older than the age threshold.
is_stale() {
    local create_epoch
    create_epoch=$(ts_to_epoch "$1")
    if (( create_epoch > 0 && create_epoch < threshold_seconds )); then
        return 0
    fi
    return 1
}

# Read "name<TAB>creationTimestamp" lines from stdin. Delete resources whose
# name matches $1 (bash regex) and that are older than the threshold. Remaining
# args are the delete command prefix (resource name and -q are appended).
process_resource_list() {
    local pattern="$1"
    shift
    local deleted=0
    local skipped=0

    while IFS=$'\t' read -r name create_time; do
        [[ -z "${name}" ]] && continue
        [[ "${name}" =~ ${pattern} ]] || continue
        if is_stale "${create_time}"; then
            if "$@" "${name}" -q; then
                echo "  deleted: ${name}"
                deleted=$(( deleted + 1 ))
            else
                echo "  FAILED:  ${name}"
                overall_failed=1
            fi
        else
            echo "  skipped: ${name}"
            skipped=$(( skipped + 1 ))
        fi
    done

    echo "  summary: ${deleted} deleted, ${skipped} skipped"
}

# ---------------------------------------------------------------------------
# 1. GKE clusters matching ^k8s-mdb-
# ---------------------------------------------------------------------------
echo "=== GKE clusters (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
clusters_deleted=0
clusters_skipped=0
while IFS=$'\t' read -r name location create_time; do
    [[ -z "${name}" ]] && continue
    [[ "${name}" =~ ^k8s-mdb- ]] || continue
    if is_stale "${create_time}"; then
        if gcloud container clusters delete "${name}" --project="${MDB_GKE_PROJECT}" --location="${location}" -q; then
            echo "  deleted: ${name} (${location})"
            clusters_deleted=$(( clusters_deleted + 1 ))
        else
            echo "  FAILED:  ${name} (${location})"
            overall_failed=1
        fi
    else
        echo "  skipped: ${name} (${location})"
        clusters_skipped=$(( clusters_skipped + 1 ))
    fi
done < <(gcloud container clusters list --project="${MDB_GKE_PROJECT}" --format="value(name,location,createTime)" 2>/dev/null || true)
echo "  summary: ${clusters_deleted} deleted, ${clusters_skipped} skipped"

# ---------------------------------------------------------------------------
# 2. DNS managed zones matching ^mongodb-mongodb-
# ---------------------------------------------------------------------------
echo "=== DNS managed zones (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
zones_deleted=0
zones_skipped=0
while IFS=$'\t' read -r zone_name create_time; do
    [[ -z "${zone_name}" ]] && continue
    [[ "${zone_name}" =~ ^mongodb-mongodb- ]] || continue
    if is_stale "${create_time}"; then
        # Delete all record sets except NS/SOA (mirrors
        # ra-09_9100_delete_dns_zone.sh).
        gcloud dns record-sets list --zone="${zone_name}" --project="${MDB_GKE_PROJECT}" --format=json 2>/dev/null \
            | jq -c '.[]' 2>/dev/null | while read -r record; do
                rs_name=$(echo "${record}" | jq -r '.name')
                rs_type=$(echo "${record}" | jq -r '.type')
                if [[ "${rs_type}" != "NS" && "${rs_type}" != "SOA" ]]; then
                    gcloud dns record-sets delete "${rs_name}" --zone="${zone_name}" --project="${MDB_GKE_PROJECT}" --type="${rs_type}" -q || true
                fi
            done || true
        if gcloud dns managed-zones delete "${zone_name}" --project="${MDB_GKE_PROJECT}" -q; then
            echo "  deleted: ${zone_name}"
            zones_deleted=$(( zones_deleted + 1 ))
        else
            echo "  FAILED:  ${zone_name}"
            overall_failed=1
        fi
    else
        echo "  skipped: ${zone_name}"
        zones_skipped=$(( zones_skipped + 1 ))
    fi
done < <(gcloud dns managed-zones list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTime)" 2>/dev/null || true)
echo "  summary: ${zones_deleted} deleted, ${zones_skipped} skipped"

# ---------------------------------------------------------------------------
# 3. Global LB resources (in dependency order)
# ---------------------------------------------------------------------------
echo "=== Forwarding rules (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-forwarding-rule' gcloud compute forwarding-rules delete --global --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute forwarding-rules list --global --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== Target HTTPS proxies (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-lb-proxy' gcloud compute target-https-proxies delete --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute target-https-proxies list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== URL maps (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-url-map' gcloud compute url-maps delete --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute url-maps list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== Backend services (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-backend-service' gcloud compute backend-services delete --global --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute backend-services list --global --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== Health checks (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-healthcheck' gcloud compute health-checks delete --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute health-checks list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== SSL certificates (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^om-certificate' gcloud compute ssl-certificates delete --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute ssl-certificates list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

echo "=== Firewall rules (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
process_resource_list '^fw-ops-manager-hc' gcloud compute firewall-rules delete --project="${MDB_GKE_PROJECT}" \
    < <(gcloud compute firewall-rules list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>/dev/null || true)

exit "${overall_failed}"
