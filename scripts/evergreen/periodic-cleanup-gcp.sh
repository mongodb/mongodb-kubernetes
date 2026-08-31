#!/usr/bin/env bash
# Periodically deletes everything in the test GCP project older than
# AGE_THRESHOLD_HOURS, except a small denylist of shared infrastructure.
#
# The project is exclusively used by ephemeral e2e test infrastructure:
# audit logs (verified Aug 2026) show all resource creation traces to the
# k8s-operator-e2e-tests service account or the GKE control planes of the
# clusters it creates. Age is therefore the only filter needed. Resources
# still in use cannot be deleted by gcloud, so delete failures are tolerated
# and retried on the next run. Clusters are deleted first so dependent
# resources are released before their own delete is attempted.
set -euo pipefail

AGE_THRESHOLD_HOURS="${AGE_THRESHOLD_HOURS:-24}"
DRY_RUN="${DRY_RUN:-false}"
: "${MDB_GKE_PROJECT:?MDB_GKE_PROJECT is required}"
if ! [[ "${AGE_THRESHOLD_HOURS}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: AGE_THRESHOLD_HOURS must be a positive integer" >&2
    exit 1
fi
case "${DRY_RUN}" in true|false) ;; *) echo "ERROR: DRY_RUN must be 'true' or 'false'" >&2; exit 1 ;; esac
threshold_epoch=$(( $(date -u +%s) - AGE_THRESHOLD_HOURS * 3600 ))
overall_failed=0

try_delete() {
    local stderr rc=0
    if [[ "${DRY_RUN}" == true ]]; then printf '  dry-run:' >&2; printf ' %q' "$@" >&2; printf '\n' >&2; return 0; fi
    printf '  running:' >&2; printf ' %q' "$@" >&2; printf '\n' >&2
    stderr=$("$@" 2>&1 1>/dev/null) || rc=$?
    (( rc == 0 )) && return 0
    grep -qiE 'not found|NOT_FOUND|notFound|does not exist|not present' <<<"${stderr}" && return 0
    printf '  stderr: %s\n' "${stderr}" >&2
    return 1
}

# gcloud emits empty fields for unset attributes (e.g. region/zone on global
# resources); bash read would collapse the resulting consecutive tabs and
# misalign positional fields. Normalize empties to "-" placeholders.
denormalize_tsv() {
    awk 'BEGIN{FS=OFS="\t"} {for(i=1;i<=NF;i++) if($i=="") $i="-"; print}'
}

# filter_old: reads TSV rows whose LAST field is a creation epoch (as emitted
# by gcloud's creationTimestamp.date('%s')) and emits only rows older than the
# threshold. Fails closed: a blank row or a non-numeric epoch fails the whole
# function, and the caller must not trust partial output (no deletions for
# that class).
filter_old() {
    local line ts
    while IFS= read -r line; do
        ts=${line##*$'\t'}
        ts=${ts%%.*}
        [[ "${ts}" =~ ^[0-9]+$ ]] || return 1
        (( ts <= threshold_epoch )) || continue
        printf '%s\n' "${line}"
    done
}

# Generic compute reaper: reap_compute <resource> <global kind>
# kind is "global-flag" (unscoped resources need an explicit --global, e.g.
# forwarding-rules) or "no-flag" (global by default, e.g. http-health-checks).
# Zonal and regional resources are detected from the zone/region columns.
reap_compute() {
    local resource=$1 kind=$2 rows name region zone
    if ! rows=$(gcloud compute "${resource}" list --project="${MDB_GKE_PROJECT}" --format="value(name,region,zone,creationTimestamp.date('%s'))" | denormalize_tsv | filter_old); then overall_failed=1; return 0; fi
    [[ -n "${rows}" ]] || return 0
    while IFS=$'\t' read -r name region zone _; do
        local args=()
        if [[ "${zone}" != "-" ]]; then args=(--zone="${zone##*/}")
        elif [[ "${region}" != "-" ]]; then args=(--region="${region##*/}")
        elif [[ "${kind}" == global-flag ]]; then args=(--global)
        fi
        try_delete gcloud compute "${resource}" delete "${name}" ${args[@]+"${args[@]}"} --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
    done <<<"${rows}"
}

reap_clusters() {
    local rows name location pid
    local pids=()
    if ! rows=$(gcloud container clusters list --project="${MDB_GKE_PROJECT}" --format="value(name,location,createTime.date('%s'))" | filter_old); then overall_failed=1; return 0; fi
    [[ -n "${rows}" ]] || return 0
    # Cluster deletes take minutes each; run them in parallel so a large
    # backlog cannot push the task past its timeout before the dependent
    # load-balancer resources are reached.
    while IFS=$'\t' read -r name location _; do
        try_delete gcloud container clusters delete "${name}" --project="${MDB_GKE_PROJECT}" --location="${location}" -q &
        pids+=("$!")
    done <<<"${rows}"
    for pid in "${pids[@]}"; do
        wait "${pid}" || overall_failed=1
    done
}

# Firewall rules have no scope flag; the default-* VPC rules are shared
# infrastructure and are never touched.
reap_firewall_rules() {
    local rows name
    if ! rows=$(gcloud compute firewall-rules list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp.date('%s'))" | filter_old); then overall_failed=1; return 0; fi
    [[ -n "${rows}" ]] || return 0
    while IFS=$'\t' read -r name _; do
        [[ "${name}" == default-* ]] && continue
        try_delete gcloud compute firewall-rules delete "${name}" --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
    done <<<"${rows}"
}

reap_dns_zones() {
    local rows zones zone name type extra parse_failed zone_failed i
    local record_names=() record_types=()
    if ! zones=$(gcloud dns managed-zones list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTime.date('%s'))" | filter_old); then overall_failed=1; return 0; fi
    [[ -n "${zones}" ]] || return 0
    while IFS=$'\t' read -r zone _; do
        if ! rows=$(gcloud dns record-sets list --zone="${zone}" --project="${MDB_GKE_PROJECT}" --format='value(name,type)'); then
            overall_failed=1
            continue
        fi
        record_names=(); record_types=(); parse_failed=0
        while IFS=$'\t' read -r name type extra; do
            [[ -z "${extra}" && -n "${name}" && -n "${type}" ]] || { parse_failed=1; continue; }
            [[ "${type}" == NS || "${type}" == SOA ]] || { record_names+=("${name}"); record_types+=("${type}"); }
        done <<<"${rows}"
        if (( parse_failed )); then
            overall_failed=1
            continue
        fi
        zone_failed=0
        for i in "${!record_names[@]}"; do
            try_delete gcloud dns record-sets delete "${record_names[i]}" --zone="${zone}" --project="${MDB_GKE_PROJECT}" --type="${record_types[i]}" -q || { overall_failed=1; zone_failed=1; }
        done
        if (( zone_failed == 0 )); then
            try_delete gcloud dns managed-zones delete "${zone}" --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
        fi
    done <<<"${zones}"
}

# sa_keys_all_old: reads single-column key validAfterTime epoch rows. Returns
# 0 only when at least one user-managed key exists and every key is old;
# 1 when the account is in use (no keys or a young key); 2 on a malformed
# epoch (caller treats as failure).
sa_keys_all_old() {
    local ts count=0
    while IFS= read -r ts; do
        ts=${ts%%.*}
        [[ -n "${ts}" ]] || continue
        [[ "${ts}" =~ ^[0-9]+$ ]] || return 2
        count=$((count + 1))
        (( ts <= threshold_epoch )) || return 1
    done
    (( count > 0 )) || return 1
    return 0
}

# Service accounts have no creation timestamp; age is established by
# requiring at least one user-managed key and all keys being old. The
# e2e-infrastructure account itself is denied.
# Known gap: an account with NO user-managed keys has no age signal and is
# skipped forever (an interrupted ra-09 run can leave one behind — SA and key
# are created in separate steps). Deleting keyless accounts blindly could hit
# a test mid-setup, so they are left for manual cleanup.
reap_service_accounts() {
    local rows email key_rows rc
    if ! rows=$(gcloud iam service-accounts list --project="${MDB_GKE_PROJECT}" --format='value(email)'); then overall_failed=1; return 0; fi
    [[ -n "${rows}" ]] || return 0
    while IFS= read -r email; do
        [[ "${email}" == k8s-operator-e2e-tests@* || "${email}" == *@developer.gserviceaccount.com ]] && continue
        if ! key_rows=$(gcloud iam service-accounts keys list --iam-account="${email}" --project="${MDB_GKE_PROJECT}" --managed-by=user --format="value(validAfterTime.date('%s'))"); then
            overall_failed=1
            continue
        fi
        rc=0; sa_keys_all_old <<<"${key_rows}" || rc=$?
        if (( rc == 2 )); then overall_failed=1; continue; fi
        (( rc == 1 )) && continue
        if try_delete gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" --member="serviceAccount:${email}" --role=roles/dns.admin -q; then
            try_delete gcloud iam service-accounts delete "${email}" --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
        else
            overall_failed=1
        fi
    done <<<"${rows}"
}

reap_clusters
reap_compute forwarding-rules global-flag
reap_compute target-pools global-flag
reap_compute backend-services global-flag
# Health checks are listed directly: after the pools and backend services
# above are gone, nothing references them and age is the only check.
reap_compute http-health-checks no-flag
reap_compute health-checks global-flag
reap_compute target-https-proxies global-flag
reap_compute url-maps global-flag
reap_compute ssl-certificates global-flag
reap_compute addresses global-flag
reap_compute network-endpoint-groups no-flag
reap_firewall_rules
reap_compute disks no-flag
reap_dns_zones
reap_service_accounts
exit "${overall_failed}"
