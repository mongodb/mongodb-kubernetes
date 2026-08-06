#!/usr/bin/env bash
set -euo pipefail

# Periodic GCP garbage collector for MongoDB Kubernetes code-snippet CI (KUBE-268).
#
# Deletes GKE clusters, DNS managed zones, global/regional load-balancer resources, and
# IAM service accounts that are older than AGE_THRESHOLD_HOURS and match the
# code-snippet naming patterns. Reclaims resources leaked by failed or
# interrupted CI runs without touching resources from in-flight runs.
#
# Env:
#   AGE_THRESHOLD_HOURS  Hours of age after which a resource is eligible (default: 24).
#   MDB_GKE_PROJECT      GCP project ID (required).
#   MDB_GKE_REGION       GCP region used by code-snippet resources (default: europe-central2).
#   DRY_RUN              List candidates and print mutations without executing them (default: false).

AGE_THRESHOLD_HOURS="${AGE_THRESHOLD_HOURS:-24}"
MDB_GKE_REGION="${MDB_GKE_REGION:-europe-central2}"
DRY_RUN="${DRY_RUN:-false}"
: "${MDB_GKE_PROJECT:?MDB_GKE_PROJECT is required}"

# Validate AGE_THRESHOLD_HOURS to prevent arithmetic injection / accidental
# threshold of zero (which would mark every matching resource as stale).
if ! [[ "${AGE_THRESHOLD_HOURS}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: AGE_THRESHOLD_HOURS must be a positive integer, got: '${AGE_THRESHOLD_HOURS}'" >&2
    exit 1
fi
if ! [[ "${MDB_GKE_REGION}" =~ ^[a-z0-9-]+$ ]]; then
    echo "ERROR: MDB_GKE_REGION must be a valid GCP region, got: '${MDB_GKE_REGION}'" >&2
    exit 1
fi
case "${DRY_RUN}" in
    true|false) ;;
    *)
        echo "ERROR: DRY_RUN must be 'true' or 'false', got: '${DRY_RUN}'" >&2
        exit 1
        ;;
esac

# Detect the date implementation once so the server-side age filter works on
# both GNU/Linux and macOS/BSD.
date_flavor=bsd
if date -u -d "2026-01-01T00:00:00Z" +%s >/dev/null 2>&1; then
    date_flavor=gnu
fi

threshold_seconds=$(( $(date -u +%s) - AGE_THRESHOLD_HOURS * 3600 ))
if [[ "${date_flavor}" == gnu ]]; then
    threshold_timestamp=$(date -u -d "@${threshold_seconds}" '+%Y-%m-%dT%H:%M:%SZ')
else
    threshold_timestamp=$(date -u -r "${threshold_seconds}" '+%Y-%m-%dT%H:%M:%SZ')
fi
overall_failed=0
delete_action=deleting
delete_summary=deleted
if [[ "${DRY_RUN}" == true ]]; then
    delete_action="would delete"
    delete_summary="would delete"
    echo "=== DRY RUN: no delete or IAM mutation commands will execute ==="
fi
zone_url_pattern='^https://[^/]+/compute/v1/projects/([^/]+)/zones/([a-z0-9-]+)$'

kubernetes_service_filter='description~kubernetes.io/service-name'

zone_name_from_url() {
    local zone_url="$1" zone_project zone_name
    if [[ "${zone_url}" =~ ${zone_url_pattern} ]]; then
        zone_project="${BASH_REMATCH[1]}"
        zone_name="${BASH_REMATCH[2]}"
        [[ "${zone_project}" == "${MDB_GKE_PROJECT}" ]] || return 1
        printf '%s\n' "${zone_name}"
        return 0
    fi
    if [[ "${zone_url}" =~ ^[a-z0-9]+(-[a-z0-9]+)+-[a-z]$ ]]; then
        printf '%s\n' "${zone_url}"
        return 0
    fi
    return 1
}

# Attempt to delete a resource. Returns 0 on success or if the resource is
# already gone (concurrent delete by a run's own teardown), 1 on genuine
# failure. Prints captured stderr for diagnostics on failure.
try_delete() {
    local rc=0 stderr_capture
    if [[ "${DRY_RUN}" == true ]]; then
        printf '  dry-run:' >&2
        printf ' %q' "$@" >&2
        printf '\n' >&2
        return 0
    fi
    printf '  running:' >&2
    printf ' %q' "$@" >&2
    printf '\n' >&2
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

# gcloud can return success after a paginated list partially fails. Treat
# permission and partial-request warnings as inventory failures so an empty
# result cannot be mistaken for a clean project.
run_gcloud_inventory() {
    local stderr_file output rc=0
    stderr_file=$(mktemp)
    output=$("$@" 2>"${stderr_file}") || rc=$?
    cat "${stderr_file}" >&2
    if (( rc != 0 )) || grep -qE "Some requests did not succeed|Required '[^']+' permission" "${stderr_file}"; then
        rm -f "${stderr_file}"
        return 1
    fi
    rm -f "${stderr_file}"
    printf '%s' "${output}"
}

delete_cluster_batch() {
    local location="$1" count
    shift
    count=$#
    [[ -n "${location}" && ${count} -gt 0 ]] || return 0
    echo "  ${delete_action} ${count} GKE clusters in ${location}"
    if try_delete gcloud container clusters delete "$@" --project="${MDB_GKE_PROJECT}" --location="${location}" -q; then
        clusters_deleted=$(( clusters_deleted + count ))
    else
        echo "  FAILED GKE cluster batch in ${location}; the next run will retry"
        clusters_failed=$(( clusters_failed + count ))
        overall_failed=1
    fi
}

# ---------------------------------------------------------------------------
# 1. GKE clusters matching ^k8s-mdb- (bulk delete per location)
# ---------------------------------------------------------------------------
echo "=== GKE clusters (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
clusters_deleted=0
clusters_skipped=0
clusters_failed=0
cluster_batch_location=""
cluster_batch_names=()
if ! cluster_list=$(run_gcloud_inventory gcloud container clusters list \
    --project="${MDB_GKE_PROJECT}" \
    --filter="name~^k8s-mdb- AND createTime < ${threshold_timestamp}" \
    --sort-by=location \
    --format="value(name,location)"); then
    echo "  ERROR listing stale GKE clusters"
    overall_failed=1
else
    while IFS=$'\t' read -r name location; do
        [[ -z "${name}" ]] && continue
        if [[ -n "${cluster_batch_location}" && "${location}" != "${cluster_batch_location}" ]]; then
            delete_cluster_batch "${cluster_batch_location}" "${cluster_batch_names[@]}"
            cluster_batch_names=()
        fi
        cluster_batch_location="${location}"
        cluster_batch_names+=("${name}")
    done <<< "${cluster_list}"
    if [[ -n "${cluster_batch_location}" ]]; then
        delete_cluster_batch "${cluster_batch_location}" "${cluster_batch_names[@]}"
    fi
fi
echo "  summary: ${clusters_deleted} ${delete_summary}, ${clusters_skipped} skipped, ${clusters_failed} failed"

# ---------------------------------------------------------------------------
# 2. DNS managed zones matching ^mongodb-[a-z0-9] (any suffixed zone; the
#    bare "mongodb" docs zone is preserved)
# ---------------------------------------------------------------------------
echo "=== DNS managed zones (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
zones_deleted=0
zones_skipped=0
zones_failed=0
if ! zone_list=$(run_gcloud_inventory gcloud dns managed-zones list \
    --project="${MDB_GKE_PROJECT}" \
    --filter="name~^mongodb-[a-z0-9] AND creationTime < ${threshold_timestamp}" \
    --format="value(name)"); then
    echo "  ERROR listing stale DNS zones"
    overall_failed=1
else
    while IFS= read -r zone_name; do
        [[ -z "${zone_name}" ]] && continue
        # DNS zones must be empty before deletion; record-set deletes remain
        # per-record because the CLI does not accept multiple zone names here.
        if ! rs_list=$(run_gcloud_inventory gcloud dns record-sets list --zone="${zone_name}" --project="${MDB_GKE_PROJECT}" --format="value(name,type)"); then
            echo "  ERROR listing record-sets for zone ${zone_name}"
            overall_failed=1
            zones_failed=$(( zones_failed + 1 ))
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
            zones_deleted=$(( zones_deleted + 1 ))
        else
            echo "  FAILED:  ${zone_name}"
            zones_failed=$(( zones_failed + 1 ))
            overall_failed=1
        fi
    done <<< "${zone_list}"
fi
echo "  summary: ${zones_deleted} ${delete_summary}, ${zones_skipped} skipped, ${zones_failed} failed"

# ---------------------------------------------------------------------------
# 3. Global LB resources (in dependency order)
# ---------------------------------------------------------------------------
# Helper: list stale resources by server-side name/age filter and bulk delete.
# $1=display, $2=regex, $3=gcloud resource type, remaining args are extra
# gcloud flags (e.g. --global) shared by both list and delete.
reap_compute_resource() {
    local display="$1" pattern="$2" resource="$3" list_output
    shift 3
    echo "=== ${display} (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
    if ! list_output=$(run_gcloud_inventory gcloud compute "${resource}" list "$@" \
        --project="${MDB_GKE_PROJECT}" \
        --filter="name~${pattern} AND creationTimestamp < ${threshold_timestamp}" \
        --format="value(name)"); then
        echo "  ERROR listing ${display}"
        overall_failed=1
    else
        local names=()
        while IFS= read -r name; do
            [[ -z "${name}" ]] && continue
            names+=("${name}")
        done <<< "${list_output}"
        if ((${#names[@]} == 0)); then
            echo "  no stale resources found"
            echo "  summary: 0 ${delete_summary}, 0 failed"
            return
        fi
        echo "  ${delete_action} ${#names[@]} ${display}"
        if try_delete gcloud compute "${resource}" delete "${names[@]}" "$@" --project="${MDB_GKE_PROJECT}" -q; then
            echo "  summary: ${#names[@]} ${delete_summary}, 0 failed"
        else
            echo "  FAILED ${display} batch; the next run will retry"
            echo "  summary: 0 deleted, ${#names[@]} failed"
            overall_failed=1
        fi
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
# 3b. Regional GKE LoadBalancer forwarding rules and target pools.
# ---------------------------------------------------------------------------
echo "=== Regional GKE LoadBalancer forwarding rules and target pools (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
gke_forwarding_rules_deleted=0
gke_forwarding_rules_skipped=0
gke_forwarding_rules_failed=0
gke_target_pools_deleted=0
gke_target_pools_skipped=0
gke_target_pools_failed=0
stale_forwarding_rule_names=()
region="${MDB_GKE_REGION}"

echo "  using GCP region: ${region}"
echo "  listing stale Kubernetes forwarding rules: ${region} (waiting for inventory)"
if ! regional_forwarding_rule_list=$(run_gcloud_inventory gcloud compute forwarding-rules list \
    --regions="${region}" \
    --project="${MDB_GKE_PROJECT}" \
    --filter="${kubernetes_service_filter} AND creationTimestamp < ${threshold_timestamp}" \
    --format="value(name)"); then
    echo "  ERROR listing stale Kubernetes forwarding rules in ${region}"
    overall_failed=1
else
    while IFS= read -r name; do
        [[ -z "${name}" ]] && continue
        stale_forwarding_rule_names+=("${name}")
    done <<< "${regional_forwarding_rule_list}"

    if ((${#stale_forwarding_rule_names[@]} > 0)); then
        echo "  ${delete_action} ${#stale_forwarding_rule_names[@]} forwarding rules in ${region}"
        if try_delete gcloud compute forwarding-rules delete "${stale_forwarding_rule_names[@]}" --region="${region}" --project="${MDB_GKE_PROJECT}" -q; then
            gke_forwarding_rules_deleted=${#stale_forwarding_rule_names[@]}
        else
            echo "  FAILED forwarding-rule batch; the next run will retry"
            gke_forwarding_rules_failed=${#stale_forwarding_rule_names[@]}
            overall_failed=1
        fi
    else
        echo "  no stale forwarding rules found in ${region}"
    fi

    echo "  listing stale Kubernetes target pools: ${region}"
    if ! regional_target_pool_list=$(run_gcloud_inventory gcloud compute target-pools list \
        --regions="${region}" \
        --project="${MDB_GKE_PROJECT}" \
        --filter="${kubernetes_service_filter} AND creationTimestamp < ${threshold_timestamp}" \
        --format="value(name)"); then
        echo "  ERROR listing stale Kubernetes target pools in ${region}"
        overall_failed=1
    else
        stale_target_pool_names=()
        while IFS= read -r name; do
            [[ -z "${name}" ]] && continue
            stale_target_pool_names+=("${name}")
        done <<< "${regional_target_pool_list}"

        if ((${#stale_target_pool_names[@]} > 0)); then
            echo "  ${delete_action} ${#stale_target_pool_names[@]} target pools in ${region}"
            if try_delete gcloud compute target-pools delete "${stale_target_pool_names[@]}" --region="${region}" --project="${MDB_GKE_PROJECT}" -q; then
                gke_target_pools_deleted=${#stale_target_pool_names[@]}
            else
                echo "  FAILED target-pool batch; the next run will retry"
                gke_target_pools_failed=${#stale_target_pool_names[@]}
                overall_failed=1
            fi
        else
            echo "  no stale target pools found in ${region}"
        fi
    fi
fi
echo "  forwarding-rule summary: ${gke_forwarding_rules_deleted} ${delete_summary}, ${gke_forwarding_rules_skipped} skipped, ${gke_forwarding_rules_failed} failed"
echo "  target-pool summary: ${gke_target_pools_deleted} ${delete_summary}, ${gke_target_pools_skipped} skipped, ${gke_target_pools_failed} failed"

# ---------------------------------------------------------------------------
# 3c. GKE LoadBalancer firewall rules (k8s-fw-*, k8s-*-hc). GKE auto-deletes
#     its own managed rules on cluster delete, but LB Service rules are left
#     behind and accumulate until the project FIREWALLS quota (500) is
#     exhausted. These carry the node network tag gke-<cluster>-<hash>-node,
#     so match on that prefix. The clusters they belonged to are already gone,
#     so there's no creationTimestamp to age-check — delete all matching.
# ---------------------------------------------------------------------------
echo "=== GKE LB firewall rules (k8s-fw-*, k8s-*-hc) ==="
fw_deleted=0
fw_failed=0
if ! fw_list=$(run_gcloud_inventory gcloud compute firewall-rules list \
    --project="${MDB_GKE_PROJECT}" \
    --filter="name~^k8s-fw- OR name~^k8s-.*-hc$" \
    --format="value(name)"); then
    echo "  ERROR listing GKE LB firewall rules"
    overall_failed=1
else
    fw_names=()
    while IFS= read -r fw_name; do
        [[ -z "${fw_name}" ]] && continue
        fw_names+=("${fw_name}")
    done <<< "${fw_list}"
    if ((${#fw_names[@]} == 0)); then
        echo "  none found"
    elif try_delete gcloud compute firewall-rules delete "${fw_names[@]}" --project="${MDB_GKE_PROJECT}" -q; then
        fw_deleted=${#fw_names[@]}
    else
        echo "  FAILED GKE LB firewall-rule batch; the next run will retry"
        fw_failed=${#fw_names[@]}
        overall_failed=1
    fi
fi
echo "  summary: ${fw_deleted} ${delete_summary}, ${fw_failed} failed"

# ---------------------------------------------------------------------------
# 3d. Orphaned persistent disks. PVCs with reclaimPolicy: Retain leave disks
#     after cluster deletion. Match code-snippet PVC namespaces and age-check
#     by lastDetachTimestamp.
# ---------------------------------------------------------------------------
echo "=== Orphaned persistent disks (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
disks_deleted=0
disks_skipped=0
disks_failed=0

delete_disk_batch() {
    local zone="$1" count
    shift
    count=$#
    [[ -n "${zone}" && ${count} -gt 0 ]] || return 0
    echo "  ${delete_action} ${count} disks in ${zone}"
    if try_delete gcloud compute disks delete "$@" --zone="${zone}" --project="${MDB_GKE_PROJECT}" -q; then
        disks_deleted=$(( disks_deleted + count ))
    else
        echo "  FAILED disk batch in ${zone}; the next run will retry"
        disks_failed=$(( disks_failed + count ))
        overall_failed=1
    fi
}

batch_zone=""
batch_names=()
echo "  listing detached CSI/PVC disk inventory (waiting for inventory)"
if ! disk_list=$(run_gcloud_inventory gcloud compute disks list \
    --project="${MDB_GKE_PROJECT}" \
    --sort-by=zone \
    --filter="name~^pvc-[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ AND description~storage.gke.io/created-by AND description~pd.csi.storage.gke.io AND description~kubernetes.io/created-for/pvc/namespace AND description~mongodb AND status=READY AND lastDetachTimestamp < ${threshold_timestamp}" \
    --format="value(name,zone,users)"); then
    echo "  ERROR listing stale detached CSI/PVC disks"
    overall_failed=1
else
    echo "  grouping filtered disk names by zone"
    while IFS=$'\t' read -r disk_name disk_zone disk_users; do
        [[ -z "${disk_name}" ]] && continue
        [[ -z "${disk_users}" ]] || continue
        if ! disk_zone_name=$(zone_name_from_url "${disk_zone}"); then
            echo "  skipped: ${disk_name} (invalid zone: ${disk_zone})"
            disks_skipped=$(( disks_skipped + 1 ))
            continue
        fi
        if [[ -n "${batch_zone}" && "${disk_zone_name}" != "${batch_zone}" ]]; then
            delete_disk_batch "${batch_zone}" "${batch_names[@]}"
            batch_names=()
        fi
        batch_zone="${disk_zone_name}"
        batch_names+=("${disk_name}")
    done <<< "${disk_list}"
    if [[ -n "${batch_zone}" ]]; then
        delete_disk_batch "${batch_zone}" "${batch_names[@]}"
    fi
fi
echo "  summary: ${disks_deleted} ${delete_summary}, ${disks_skipped} skipped, ${disks_failed} failed"

# ---------------------------------------------------------------------------
# 4. IAM service accounts matching ^ext-dns-sa- (created by ra-09_0100,
#    granted project-wide roles/dns.admin by ra-09_0120, key created by
#    ra-09_0130). Killed runs leak the SA, the IAM binding, and the key.
# ---------------------------------------------------------------------------
echo "=== IAM service accounts (threshold: ${AGE_THRESHOLD_HOURS}h) ==="
sas_deleted=0
sas_skipped=0
if ! sa_list=$(run_gcloud_inventory gcloud iam service-accounts list \
    --project="${MDB_GKE_PROJECT}" \
    --filter="email~^ext-dns-sa-" \
    --format="value(email)"); then
    echo "  ERROR listing service accounts"
    overall_failed=1
else
    while IFS= read -r sa_email; do
        [[ -z "${sa_email}" ]] && continue
        # A stale user-managed key is the creation-time proxy for the SA.
        # SAs with no stale user-managed keys are skipped to avoid a race
        # between SA creation and key creation during an in-flight run.
        if ! sa_key_list=$(run_gcloud_inventory gcloud iam service-accounts keys list \
            --iam-account="${sa_email}" \
            --project="${MDB_GKE_PROJECT}" \
            --filter="keyType=USER_MANAGED AND validAfterTime < ${threshold_timestamp}" \
            --format="value(name)" \
            --sort-by="validAfterTime" \
            --limit=1); then
            echo "  ERROR listing keys for ${sa_email}"
            overall_failed=1
            continue
        fi
        if [[ -z "${sa_key_list}" ]]; then
            echo "  skipped: ${sa_email} (no user-managed keys)"
            sas_skipped=$(( sas_skipped + 1 ))
            continue
        fi

        # Remove project IAM binding first (may already be gone).
        try_delete gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" \
            --member serviceAccount:"${sa_email}" --role roles/dns.admin -q || true
        if try_delete gcloud iam service-accounts delete "${sa_email}" --project="${MDB_GKE_PROJECT}" -q; then
            echo "  ${delete_summary}: ${sa_email}"
            sas_deleted=$(( sas_deleted + 1 ))
        else
            echo "  FAILED:  ${sa_email}"
            overall_failed=1
        fi
    done <<< "${sa_list}"
fi
echo "  summary: ${sas_deleted} ${delete_summary}, ${sas_skipped} skipped"

exit "${overall_failed}"
