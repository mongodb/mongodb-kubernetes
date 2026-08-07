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
date_flavor=bsd
if date -u -d "2026-01-01T00:00:00Z" +%s >/dev/null 2>&1; then date_flavor=gnu; fi
threshold_epoch=$(( $(date -u +%s) - AGE_THRESHOLD_HOURS * 3600 ))
overall_failed=0

timestamp_epoch() {
    local timestamp="${1%%.*}"
    [[ "${timestamp}" == *Z ]] || timestamp+=Z
    [[ "${timestamp}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] || return 1
    if [[ "${date_flavor}" == gnu ]]; then date -u -d "${timestamp}" +%s; else date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "${timestamp}" +%s; fi
}
run_inventory() {
    local stderr output rc=0
    stderr=$(mktemp)
    output=$("$@" 2>"${stderr}") || rc=$?
    if (( rc != 0 )); then
        cat "${stderr}" >&2
        rm -f "${stderr}"; return 1
    fi
    if [[ -s "${stderr}" ]]; then cat "${stderr}" >&2; fi
    rm -f "${stderr}"
    printf '%s' "${output}"
}
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

# Generic compute reaper: reap_compute <resource> <location kind>
# where kind is: region-or-global | zone-or-region | zone | none.
reap_compute() {
    local resource=$1 kind=$2 rows name location created extra created_epoch parse_failed=0 i
    local names=() locations=()
    local jq_fields
    case "${kind}" in
        region-or-global) jq_fields='.[] | [(.name // ""), ((.region // "") | if . == "" then "global" else split("/")[-1] end), (.creationTimestamp // "-")] | @tsv' ;;
        zone-or-region)   jq_fields='.[] | [(.name // ""), (if (.zone // "") != "" then "zone:" + ((.zone // "") | split("/")[-1]) else "region:" + ((.region // "") | split("/")[-1]) end), (.creationTimestamp // "-")] | @tsv' ;;
        zone)             jq_fields='.[] | [(.name // ""), ((.zone // "") | if . == "" then "-" else split("/")[-1] end), (.creationTimestamp // "-")] | @tsv' ;;
        none)             jq_fields='.[] | [(.name // ""), "-", (.creationTimestamp // "-")] | @tsv' ;;
    esac
    if ! rows=$(run_inventory gcloud compute "${resource}" list --project="${MDB_GKE_PROJECT}" --format=json | jq -r "${jq_fields}"); then overall_failed=1; return 0; fi
    while IFS=$'\t' read -r name location created extra; do
        [[ -z "${name}" && ( -z "${created}" || "${created}" == "-" ) ]] && continue
        [[ -z "${extra}" && -n "${name}" ]] || { parse_failed=1; continue; }
        if [[ "${created}" == "-" ]] || ! created_epoch=$(timestamp_epoch "${created}"); then parse_failed=1; continue; fi
        (( created_epoch <= threshold_epoch )) || continue
        if [[ "${kind}" == region-or-global || "${kind}" == zone ]] && [[ "${location}" == "-" ]]; then parse_failed=1; continue; fi
        names+=("${name}"); locations+=("${location}")
    done <<<"${rows}"
    if (( parse_failed )); then overall_failed=1; return 0; fi
    ((${#names[@]})) || return 0
    for i in "${!names[@]}"; do
        local args=()
        case "${locations[i]}" in
            global) args=(--global) ;;
            zone:*) args=(--zone="${locations[i]#zone:}") ;;
            region:*) args=(--region="${locations[i]#region:}") ;;
            -) ;;
            *) [[ "${kind}" == zone ]] && args=(--zone="${locations[i]}") || args=(--region="${locations[i]}") ;;
        esac
        try_delete gcloud compute "${resource}" delete "${names[i]}" ${args[@]+"${args[@]}"} --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
    done
}

reap_clusters() {
    local rows name location created extra created_epoch parse_failed=0
    if ! rows=$(run_inventory gcloud container clusters list --project="${MDB_GKE_PROJECT}" --format='value(name,location,createTime)'); then overall_failed=1; return 0; fi
    while IFS=$'\t' read -r name location created extra; do
        [[ -z "${name}" && -z "${location}" && -z "${created}" ]] && continue
        [[ -z "${extra}" && -n "${name}" && -n "${location}" && -n "${created}" ]] || { parse_failed=1; continue; }
        if ! created_epoch=$(timestamp_epoch "${created}"); then parse_failed=1; continue; fi
        (( created_epoch <= threshold_epoch )) || continue
        try_delete gcloud container clusters delete "${name}" --project="${MDB_GKE_PROJECT}" --location="${location}" -q || overall_failed=1
    done <<<"${rows}"
    (( parse_failed == 0 )) || overall_failed=1
}

# Firewall rules have no scope flag; the default-* VPC rules are shared
# infrastructure and are never touched.
reap_firewall_rules() {
    local rows name created extra created_epoch parse_failed=0
    local names=()
    if ! rows=$(run_inventory gcloud compute firewall-rules list --project="${MDB_GKE_PROJECT}" --format='value(name,creationTimestamp)'); then overall_failed=1; return 0; fi
    while IFS=$'\t' read -r name created extra; do
        [[ -z "${name}" && -z "${created}" ]] && continue
        [[ -z "${extra}" && -n "${name}" ]] || { parse_failed=1; continue; }
        [[ "${name}" == default-* ]] && continue
        if [[ -z "${created}" ]] || ! created_epoch=$(timestamp_epoch "${created}"); then parse_failed=1; continue; fi
        (( created_epoch <= threshold_epoch )) && names+=("${name}")
    done <<<"${rows}"
    if (( parse_failed )); then overall_failed=1; return 0; fi
    ((${#names[@]})) || return 0
    for name in "${names[@]}"; do try_delete gcloud compute firewall-rules delete "${name}" --project="${MDB_GKE_PROJECT}" -q || overall_failed=1; done
}

reap_dns_zones() {
    local rows name created extra created_epoch parse_failed=0 zone record_names record_types i zone_failed type
    local names=()
    if ! rows=$(run_inventory gcloud dns managed-zones list --project="${MDB_GKE_PROJECT}" --format='value(name,creationTime)'); then overall_failed=1; return 0; fi
    while IFS=$'\t' read -r name created extra; do
        [[ -z "${name}" && -z "${created}" ]] && continue
        [[ -z "${extra}" && -n "${name}" ]] || { parse_failed=1; continue; }
        if [[ -z "${created}" ]] || ! created_epoch=$(timestamp_epoch "${created}"); then parse_failed=1; continue; fi
        (( created_epoch <= threshold_epoch )) && names+=("${name}")
    done <<<"${rows}"
    if (( parse_failed )); then overall_failed=1; return 0; fi
    ((${#names[@]})) || return 0
    for zone in "${names[@]}"; do
        if ! rows=$(run_inventory gcloud dns record-sets list --zone="${zone}" --project="${MDB_GKE_PROJECT}" --format='value(name,type)'); then
            overall_failed=1
            continue
        fi
        record_names=(); record_types=(); parse_failed=0
        while IFS=$'\t' read -r name type extra; do
            [[ -z "${name}" && -z "${type}" && -z "${extra}" ]] && continue
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
    done
}

# Service accounts have no creation timestamp; age is established by
# requiring at least one user-managed key and all keys being old. The
# e2e-infrastructure account itself is denied.
reap_service_accounts() {
    local rows email extra key_rows key_created key_extra key_epoch key_parse_failed key_count all_keys_old parse_failed
    local emails=()
    if ! rows=$(run_inventory gcloud iam service-accounts list --project="${MDB_GKE_PROJECT}" --format='value(email)'); then overall_failed=1; return 0; fi
    parse_failed=0
    while IFS=$'\t' read -r email extra; do
        [[ -z "${email}" && -z "${extra}" ]] && continue
        [[ -z "${extra}" && -n "${email}" ]] || { parse_failed=1; continue; }
        [[ "${email}" == k8s-operator-e2e-tests@* || "${email}" == *@developer.gserviceaccount.com ]] && continue
        emails+=("${email}")
    done <<<"${rows}"
    if (( parse_failed )); then overall_failed=1; return 0; fi
    ((${#emails[@]})) || return 0
    for email in "${emails[@]}"; do
        if ! key_rows=$(run_inventory gcloud iam service-accounts keys list --iam-account="${email}" --project="${MDB_GKE_PROJECT}" --managed-by=user --format='value(validAfterTime)'); then
            overall_failed=1
            continue
        fi
        key_parse_failed=0; key_count=0; all_keys_old=true
        while IFS=$'\t' read -r key_created key_extra; do
            [[ -z "${key_created}" && -z "${key_extra}" ]] && continue
            [[ -z "${key_extra}" && -n "${key_created}" ]] || { key_parse_failed=1; continue; }
            if ! key_epoch=$(timestamp_epoch "${key_created}"); then key_parse_failed=1; continue; fi
            key_count=$((key_count + 1))
            (( key_epoch <= threshold_epoch )) || all_keys_old=false
        done <<<"${key_rows}"
        if (( key_parse_failed )); then
            overall_failed=1
            continue
        fi
        if (( key_count == 0 )) || [[ "${all_keys_old}" != true ]]; then continue; fi
        if try_delete gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" --member="serviceAccount:${email}" --role=roles/dns.admin -q; then
            try_delete gcloud iam service-accounts delete "${email}" --project="${MDB_GKE_PROJECT}" -q || overall_failed=1
        else
            overall_failed=1
        fi
    done
}

reap_clusters
reap_compute forwarding-rules region-or-global
reap_compute target-pools region-or-global
reap_compute backend-services region-or-global
# Health checks are listed directly: after the pools and backend services
# above are gone, nothing references them and age is the only check.
reap_compute http-health-checks none
reap_compute health-checks region-or-global
reap_compute target-https-proxies region-or-global
reap_compute url-maps region-or-global
reap_compute ssl-certificates region-or-global
reap_compute addresses region-or-global
reap_compute network-endpoint-groups zone-or-region
reap_firewall_rules
reap_compute disks zone
reap_dns_zones
reap_service_accounts
exit "${overall_failed}"
