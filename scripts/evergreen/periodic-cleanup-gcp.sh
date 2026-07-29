#!/usr/bin/env bash
set -euo pipefail

# Periodic GCP garbage collector for MongoDB Kubernetes code-snippet CI (KUBE-268).
#
# Deletes GKE clusters, DNS managed zones, global load-balancer resources, and
# IAM service accounts that are older than AGE_THRESHOLD_HOURS and match the
# code-snippet naming patterns. Reclaims resources leaked by failed or
# interrupted CI runs without touching resources from in-flight runs.
#
# Env:
#   AGE_THRESHOLD_HOURS  Hours of age after which a resource is eligible (default: 24).
#   MDB_GKE_PROJECT      GCP project ID (required).

AGE_THRESHOLD_HOURS="${AGE_THRESHOLD_HOURS:-24}"
: "${MDB_GKE_PROJECT:?MDB_GKE_PROJECT is required}"

# Validate AGE_THRESHOLD_HOURS to prevent arithmetic injection / accidental
# threshold of zero (which would mark every matching resource as stale).
if ! [[ "${AGE_THRESHOLD_HOURS}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: AGE_THRESHOLD_HOURS must be a positive integer, got: '${AGE_THRESHOLD_HOURS}'" >&2
    exit 1
fi

# Require GNU date (for -d flag). On macOS/BSD, date -d fails and every
# resource would be silently skipped — fail loudly instead.
if ! date -u -d "2026-01-01T00:00:00Z" +%s >/dev/null 2>&1; then
    echo "ERROR: GNU date is required (the -d flag is not supported on this system)." >&2
    exit 1
fi

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

# Attempt to delete a resource. Returns 0 on success or if the resource is
# already gone (concurrent delete by a run's own teardown), 1 on genuine
# failure. Prints captured stderr for diagnostics on failure.
try_delete() {
    local rc=0 stderr_capture
    stderr_capture=$("$@" 2>&1 1>/dev/null) || rc=$?
    if [[ ${rc} -eq 0 ]]; then
        return 0
    fi
    if echo "${stderr_capture}" | grep -qiE "was not found|NOT_FOUND|notFound"; then
        return 0  # already gone — concurrent delete, treat as success
    fi
    echo "  stderr: ${stderr_capture}" >&2
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
            if try_delete "$@" "${name}" -q; then
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
    return 0
}

# Delete a single GKE cluster with not-found tolerance and per-cluster logging.
# Designed to be backgrounded (&) by the caller.
delete_cluster() {
    local name="$1" location="$2"
    if try_delete gcloud container clusters delete "${name}" --project="${MDB_GKE_PROJECT}" --location="${location}" -q; then
        echo "  deleted: ${name} (${location})"
    else
        echo "  FAILED:  ${name} (${location})"
        return 1
    fi
}

# ---------------------------------------------------------------------------
# 1. GKE clusters matching ^k8s-mdb- (parallel delete to stay within timeout)
# ---------------------------------------------------------------------------
echo "=== GKE clusters (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
clusters_deleted=0
clusters_skipped=0
if ! cluster_list=$(gcloud container clusters list --project="${MDB_GKE_PROJECT}" --format="value(name,location,createTime)" 2>&1); then
    echo "  ERROR listing clusters: ${cluster_list}"
    overall_failed=1
else
    pids=()
    while IFS=$'\t' read -r name location create_time; do
        [[ -z "${name}" ]] && continue
        [[ "${name}" =~ ^k8s-mdb- ]] || continue
        if is_stale "${create_time}"; then
            echo "  deleting: ${name} (${location})"
            delete_cluster "${name}" "${location}" &
            pids+=($!)
        else
            echo "  skipped: ${name} (${location})"
            clusters_skipped=$(( clusters_skipped + 1 ))
        fi
    done <<< "${cluster_list}"

    # Wait for all parallel deletes; count successes and failures.
    if [[ ${#pids[@]} -gt 0 ]]; then
        for pid in "${pids[@]}"; do
            if wait "${pid}" 2>/dev/null; then
                clusters_deleted=$(( clusters_deleted + 1 ))
            else
                overall_failed=1
            fi
        done
    fi
fi
echo "  summary: ${clusters_deleted} deleted, ${clusters_skipped} skipped"

# ---------------------------------------------------------------------------
# 2. DNS managed zones matching ^mongodb-[a-z0-9] (any suffixed zone; the
#    bare "mongodb" docs zone is preserved)
# ---------------------------------------------------------------------------
echo "=== DNS managed zones (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
zones_deleted=0
zones_skipped=0
if ! zone_list=$(gcloud dns managed-zones list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTime)" 2>&1); then
    echo "  ERROR listing DNS zones: ${zone_list}"
    overall_failed=1
else
    while IFS=$'\t' read -r zone_name create_time; do
        [[ -z "${zone_name}" ]] && continue
        [[ "${zone_name}" =~ ^mongodb-[a-z0-9] ]] || continue
        if is_stale "${create_time}"; then
            # Delete all record sets except NS/SOA (mirrors
            # ra-09_9100_delete_dns_zone.sh). Use --format=value instead of
            # jq to avoid an unverified dependency.
            if ! rs_list=$(gcloud dns record-sets list --zone="${zone_name}" --project="${MDB_GKE_PROJECT}" --format="value(name,type)" 2>&1); then
                echo "  ERROR listing record-sets for zone ${zone_name}: ${rs_list}"
                overall_failed=1
                continue
            fi
            while IFS=$'\t' read -r rs_name rs_type; do
                [[ -z "${rs_name}" ]] && continue
                if [[ "${rs_type}" != "NS" && "${rs_type}" != "SOA" ]]; then
                    if ! try_delete gcloud dns record-sets delete "${rs_name}" --zone="${zone_name}" --project="${MDB_GKE_PROJECT}" --type="${rs_type}" -q; then
                        echo "  FAILED record-set: ${rs_name} (${rs_type})"
                        overall_failed=1
                    fi
                fi
            done <<< "${rs_list}"
            if try_delete gcloud dns managed-zones delete "${zone_name}" --project="${MDB_GKE_PROJECT}" -q; then
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
    done <<< "${zone_list}"
fi
echo "  summary: ${zones_deleted} deleted, ${zones_skipped} skipped"

# ---------------------------------------------------------------------------
# 3. Global LB resources (in dependency order)
# ---------------------------------------------------------------------------
# Helper: list, filter by pattern+age, delete with try_delete. $1=display,
# $2=regex, $3=gcloud resource type, remaining args are extra gcloud flags
# (e.g. --global) shared by both list and delete.
reap_compute_resource() {
    local display="$1" pattern="$2" resource="$3"
    shift 3
    echo "=== ${display} (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
    if ! list_output=$(gcloud compute "${resource}" list "$@" --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp)" 2>&1); then
        echo "  ERROR listing ${display}: ${list_output}"
        overall_failed=1
    else
        process_resource_list "${pattern}" gcloud compute "${resource}" delete "$@" --project="${MDB_GKE_PROJECT}" <<< "${list_output}"
    fi
}

reap_compute_resource "Forwarding rules" '^om-forwarding-rule' forwarding-rules --global
reap_compute_resource "Target HTTPS proxies" '^om-lb-proxy' target-https-proxies
reap_compute_resource "URL maps" '^om-url-map' url-maps
reap_compute_resource "Backend services" '^om-backend-service' backend-services --global
reap_compute_resource "Health checks" '^om-healthcheck' health-checks
reap_compute_resource "SSL certificates" '^om-certificate' ssl-certificates
reap_compute_resource "Firewall rules" '^fw-ops-manager-hc' firewall-rules

# ---------------------------------------------------------------------------
# 4. IAM service accounts matching ^ext-dns-sa- (created by ra-09_0100,
#    granted project-wide roles/dns.admin by ra-09_0120, key created by
#    ra-09_0130). Killed runs leak the SA, the IAM binding, and the key.
# ---------------------------------------------------------------------------
echo "=== IAM service accounts (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
sas_deleted=0
sas_skipped=0
if ! sa_list=$(gcloud iam service-accounts list --project="${MDB_GKE_PROJECT}" --format="value(email)" 2>&1); then
    echo "  ERROR listing service accounts: ${sa_list}"
    overall_failed=1
else
    while IFS= read -r sa_email; do
        [[ -z "${sa_email}" ]] && continue
        [[ "${sa_email}" =~ ^ext-dns-sa- ]] || continue
        # Use the SA key's validAfterTime as a proxy for creation time.
        # SAs with no user-managed keys are skipped (not treated as orphaned)
        # to avoid a race where the reaper runs between SA creation
        # (ra-09_0100) and key creation (ra-09_0130) during an in-flight run.
        if ! sa_key_time=$(gcloud iam service-accounts keys list --iam-account="${sa_email}" --project="${MDB_GKE_PROJECT}" --filter="keyType=USER_MANAGED" --format="value(validAfterTime)" --sort-by="validAfterTime" --limit=1 2>&1); then
            echo "  ERROR listing keys for ${sa_email}: ${sa_key_time}"
            overall_failed=1
            continue
        fi
        if [[ -z "${sa_key_time}" ]]; then
            echo "  skipped: ${sa_email} (no user-managed keys)"
            sas_skipped=$(( sas_skipped + 1 ))
            continue
        fi
        # Validate timestamp shape — with 2>&1, gcloud stderr warnings on a
        # successful list could pollute sa_key_time and cause is_stale to
        # silently skip the SA forever.
        if ! [[ "${sa_key_time}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T ]]; then
            echo "  ERROR unparseable key time for ${sa_email}: ${sa_key_time}"
            overall_failed=1
            continue
        fi
        if is_stale "${sa_key_time}"; then
            # Remove project IAM binding first (may already be gone).
            gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" \
                --member serviceAccount:"${sa_email}" --role roles/dns.admin -q >/dev/null 2>&1 || true
            if try_delete gcloud iam service-accounts delete "${sa_email}" --project="${MDB_GKE_PROJECT}" -q; then
                echo "  deleted: ${sa_email}"
                sas_deleted=$(( sas_deleted + 1 ))
            else
                echo "  FAILED:  ${sa_email}"
                overall_failed=1
            fi
        else
            echo "  skipped: ${sa_email}"
            sas_skipped=$(( sas_skipped + 1 ))
        fi
    done <<< "${sa_list}"
fi
echo "  summary: ${sas_deleted} deleted, ${sas_skipped} skipped"

exit "${overall_failed}"
