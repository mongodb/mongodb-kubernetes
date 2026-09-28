#!/usr/bin/env bash
# Periodic garbage collector for the GKE code-snippet test project (KUBE-268).
#
# Deletes every resource in MDB_GKE_PROJECT older than AGE_THRESHOLD_HOURS,
# except the small auditable denylists near the top of this file. The project
# is exclusively used by ephemeral e2e test infrastructure: Cloud Audit Logs
# (checked Aug 2026, re-checked Sep 2026 by classifying a DRY_RUN against the
# live project) show all resource creation traces to the k8s-operator-e2e-tests
# service account or the GKE control planes of the clusters it creates. Age is
# therefore the only ownership filter: the `k8s-fw-*` rules of an in-flight run
# are younger than the threshold and are never candidates, so the reaper cannot
# strip a running run's LoadBalancer ingress (the KUBE-268 outage).
#
# Safety properties:
#  - Clusters are deleted first, in parallel, so dependent load-balancer
#    resources are released before the compute reapers run.
#  - Resources still in use cannot be deleted by gcloud: individual failures
#    are tolerated and retried on the next periodic run.
#  - Deletes are batched (BATCH_SIZE names per gcloud call, grouped by
#    zone/region) and fall back to per-name deletes when a batch fails, so one
#    in-use resource cannot hide the others.
#  - filter_old fails closed: a malformed inventory aborts its class instead
#    of deleting from a partial list.
#
# Env:
#  - MDB_GKE_PROJECT      GCP project ID (required).
#  - AGE_THRESHOLD_HOURS  Age after which a resource is eligible (default: 24).
#  - DRY_RUN              Log mutations without executing them (default: false).
#  - BATCH_SIZE           Names per batched delete call (default: 50).
set -euo pipefail

AGE_THRESHOLD_HOURS="${AGE_THRESHOLD_HOURS:-24}"
DRY_RUN="${DRY_RUN:-false}"
BATCH_SIZE="${BATCH_SIZE:-50}"
: "${MDB_GKE_PROJECT:?MDB_GKE_PROJECT is required}"
if ! [[ "${AGE_THRESHOLD_HOURS}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: AGE_THRESHOLD_HOURS must be a positive integer" >&2
    exit 1
fi
if ! [[ "${BATCH_SIZE}" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: BATCH_SIZE must be a positive integer" >&2
    exit 1
fi
case "${DRY_RUN}" in true|false) ;; *) echo "ERROR: DRY_RUN must be 'true' or 'false'" >&2; exit 1 ;; esac
threshold_epoch=$(( $(date -u +%s) - AGE_THRESHOLD_HOURS * 3600 ))
overall_failed=0

if [[ "${DRY_RUN}" == true ]]; then
    echo "=== DRY RUN: no delete or IAM mutation commands will execute ===" >&2
fi

# Denylists: the reaper never deletes a resource whose name matches one of
# these EREs. Everything not listed is deleted once older than the threshold,
# so keep each list small and auditable.
FIREWALL_RULE_DENYLIST=(
    '^default-' # shared VPC networking rules
)
SERVICE_ACCOUNT_DENYLIST=(
    '^k8s-operator-e2e-tests@'            # CI infrastructure account
    '.*@developer\.gserviceaccount\.com$' # GCE default compute account
)

# Counters for the resource class currently being reaped. `listed` counts the
# inventory, `old` the candidates that passed the age/ownership checks and are
# not denylisted, `deleted`/`failed` the mutations at the class's top level
# (one cluster, one disk, one load-balancer rule, ...).
listed=0
old=0
deleted=0
failed=0

class_start() {
    listed=0
    old=0
    deleted=0
    failed=0
    printf '\n=== %s (threshold: %sh) ===\n' "$1" "${AGE_THRESHOLD_HOURS}" >&2
}

class_summary() {
    local action=deleted
    [[ "${DRY_RUN}" == true ]] && action=would-delete
    printf 'summary: %s listed, %s older than %sh, %s %s, %s failed\n' \
        "${listed}" "${old}" "${AGE_THRESHOLD_HOURS}" "${deleted}" "${action}" "${failed}" >&2
}

denylist_matches() {
    local name=$1 pattern
    shift
    for pattern in "$@"; do
        [[ "${name}" =~ ${pattern} ]] && return 0
    done
    return 1
}

count_rows() {
    awk 'NF{n++} END{print n+0}' <<<"$1"
}

# try_delete is purely a wrapper around a mutating command: it returns 0 on
# success or when the resource is already gone, 1 on genuine failure. It never
# updates the counters itself so callers can count at their own granularity.
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

# try_delete_batch <resource> <scope-flag> -- <name...>
# <scope-flag> is a single optional flag ("--zone=...", "--region=...",
# "--global") or empty for resources whose delete has no scope flag.
# Deletes up to BATCH_SIZE names per gcloud call. A failed batch is retried
# name by name so one in-use or already-gone resource cannot hide the others.
# Never fails the shell: failures are counted and retried on the next run.
try_delete_batch() {
    local resource=$1 scope_flag=$2
    shift 2
    local chunk=() i name
    [[ "${1:-}" == "--" ]] && shift
    (($#)) || return 0
    for ((i = 0; i < $#; i += BATCH_SIZE)); do
        chunk=("${@:i + 1:BATCH_SIZE}")
        if [[ "${DRY_RUN}" == true ]]; then
            printf '  dry-run: gcloud compute %s delete' "${resource}" >&2
            printf ' %q' "${chunk[@]}" >&2
            [[ -n "${scope_flag}" ]] && printf ' %q' "${scope_flag}" >&2
            printf ' --project=%q -q\n' "${MDB_GKE_PROJECT}" >&2
            deleted=$((deleted + ${#chunk[@]}))
            continue
        fi
        if gcloud compute "${resource}" delete "${chunk[@]}" ${scope_flag:+"${scope_flag}"} --project="${MDB_GKE_PROJECT}" -q; then
            deleted=$((deleted + ${#chunk[@]}))
        else
            printf '  batch delete of %s %s failed; retrying individually\n' "${#chunk[@]}" "${resource}" >&2
            for name in "${chunk[@]}"; do
                if try_delete gcloud compute "${resource}" delete "${name}" ${scope_flag:+"${scope_flag}"} --project="${MDB_GKE_PROJECT}" -q; then
                    deleted=$((deleted + 1))
                else
                    failed=$((failed + 1))
                    overall_failed=1
                fi
            done
        fi
    done
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

# group_by_scope: reads TSV rows "name region zone epoch" (as emitted by the
# compute inventory) and prints "scope name" rows sorted by scope, where scope
# is "zone:<name>", "region:<name>" or "global". Grouping makes each batched
# gcloud call target a single scope: mixing zones or regions in one delete is
# not possible.
group_by_scope() {
    awk -F'\t' 'BEGIN{OFS="\t"} {
        if ($3 != "-") {
            zone = $3;
            sub(/.*\//, "", zone);
            scope = "zone:" zone;
        } else if ($2 != "-") {
            region = $2;
            sub(/.*\//, "", region);
            scope = "region:" region;
        } else {
            scope = "global";
        }
        print scope, $1;
    }' | sort -t$'\t' -k1,1
}

# gcloud compute network-endpoint-groups delete accepts a single name per call
# even though the reaper groups that class by zone.
single_name_delete() {
    [[ "$1" == network-endpoint-groups ]]
}

# flush_compute_batch <resource> <global-kind> <scope> <name...>
flush_compute_batch() {
    local resource=$1 kind=$2 scope=$3
    shift 3
    (($#)) || return 0
    local scope_flag="" name
    case "${scope}" in
        zone:*) scope_flag="--zone=${scope#zone:}" ;;
        region:*) scope_flag="--region=${scope#region:}" ;;
        *) [[ "${kind}" == global-flag ]] && scope_flag="--global" ;;
    esac
    if single_name_delete "${resource}"; then
        for name in "$@"; do
            if try_delete gcloud compute "${resource}" delete "${name}" ${scope_flag:+"${scope_flag}"} --project="${MDB_GKE_PROJECT}" -q; then
                deleted=$((deleted + 1))
            else
                failed=$((failed + 1))
                overall_failed=1
            fi
        done
        return 0
    fi
    try_delete_batch "${resource}" "${scope_flag}" -- "$@"
}

# Generic compute reaper: reap_compute <resource> <global kind>
# kind is "global-flag" (unscoped resources need an explicit --global, e.g.
# forwarding-rules) or "no-flag" (global by default, e.g. http-health-checks).
# Zonal and regional resources are detected from the zone/region columns.
reap_compute() {
    local resource=$1 kind=$2 raw rows row_scope name
    class_start "${resource}"
    if ! raw=$(gcloud compute "${resource}" list --project="${MDB_GKE_PROJECT}" --format="value(name,region,zone,creationTimestamp.date('%s'))"); then
        echo "ERROR: failed to list ${resource}; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    if [[ -z "${raw}" ]]; then
        class_summary
        return 0
    fi
    listed=$(count_rows "${raw}")
    if ! rows=$(denormalize_tsv <<<"${raw}" | filter_old); then
        echo "ERROR: malformed ${resource} inventory; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    old=$(count_rows "${rows}")
    if [[ -z "${rows}" ]]; then
        class_summary
        return 0
    fi
    local scope="" names=()
    while IFS=$'\t' read -r row_scope name; do
        if [[ -n "${scope}" && "${row_scope}" != "${scope}" ]]; then
            flush_compute_batch "${resource}" "${kind}" "${scope}" ${names[@]+"${names[@]}"}
            names=()
        fi
        scope="${row_scope}"
        names+=("${name}")
    done < <(group_by_scope <<<"${rows}")
    flush_compute_batch "${resource}" "${kind}" "${scope}" ${names[@]+"${names[@]}"}
    class_summary
}

reap_clusters() {
    local raw rows name location pid
    local pids=()
    class_start clusters
    if ! raw=$(gcloud container clusters list --project="${MDB_GKE_PROJECT}" --format="value(name,location,createTime.date('%s'))"); then
        echo "ERROR: failed to list clusters; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    if [[ -z "${raw}" ]]; then
        class_summary
        return 0
    fi
    listed=$(count_rows "${raw}")
    if ! rows=$(filter_old <<<"${raw}"); then
        echo "ERROR: malformed cluster inventory; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    old=$(count_rows "${rows}")
    if [[ -z "${rows}" ]]; then
        class_summary
        return 0
    fi
    # Cluster deletes take minutes each; run them in parallel so a large
    # backlog cannot push the task past its timeout before the dependent
    # load-balancer resources are reached. The background subshells cannot
    # update the counters, so successes/failures are counted from wait status.
    while IFS=$'\t' read -r name location _; do
        try_delete gcloud container clusters delete "${name}" --project="${MDB_GKE_PROJECT}" --location="${location}" -q &
        pids+=("$!")
    done <<<"${rows}"
    for pid in "${pids[@]}"; do
        if wait "${pid}"; then
            deleted=$((deleted + 1))
        else
            failed=$((failed + 1))
            overall_failed=1
        fi
    done
    class_summary
}

# Firewall rules have no scope flag; the default-* VPC rules are shared
# infrastructure and are never touched (FIREWALL_RULE_DENYLIST). All stale
# rules go in one batched delete.
reap_firewall_rules() {
    local raw rows name denylisted=0
    class_start firewall-rules
    if ! raw=$(gcloud compute firewall-rules list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTimestamp.date('%s'))"); then
        echo "ERROR: failed to list firewall-rules; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    if [[ -z "${raw}" ]]; then
        class_summary
        return 0
    fi
    listed=$(count_rows "${raw}")
    if ! rows=$(filter_old <<<"${raw}"); then
        echo "ERROR: malformed firewall-rules inventory; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    old=$(count_rows "${rows}")
    if [[ -z "${rows}" ]]; then
        class_summary
        return 0
    fi
    local names=()
    while IFS=$'\t' read -r name _; do
        if denylist_matches "${name}" ${FIREWALL_RULE_DENYLIST[@]+"${FIREWALL_RULE_DENYLIST[@]}"}; then
            denylisted=$((denylisted + 1))
            continue
        fi
        names+=("${name}")
    done <<<"${rows}"
    if ((denylisted > 0)); then
        printf '  skipped %s denylisted rule(s)\n' "${denylisted}" >&2
    fi
    if ((${#names[@]})); then
        try_delete_batch firewall-rules "" -- "${names[@]}"
    fi
    class_summary
}

reap_dns_zones() {
    local raw zones zone name type extra parse_failed zone_failed i
    local record_names=() record_types=()
    class_start dns-managed-zones
    if ! raw=$(gcloud dns managed-zones list --project="${MDB_GKE_PROJECT}" --format="value(name,creationTime.date('%s'))"); then
        echo "ERROR: failed to list dns-managed-zones; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    if [[ -z "${raw}" ]]; then
        class_summary
        return 0
    fi
    listed=$(count_rows "${raw}")
    if ! zones=$(filter_old <<<"${raw}"); then
        echo "ERROR: malformed dns-managed-zones inventory; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    old=$(count_rows "${zones}")
    if [[ -z "${zones}" ]]; then
        class_summary
        return 0
    fi
    while IFS=$'\t' read -r zone _; do
        if ! rows=$(gcloud dns record-sets list --zone="${zone}" --project="${MDB_GKE_PROJECT}" --format='value(name,type)'); then
            overall_failed=1
            failed=$((failed + 1))
            continue
        fi
        record_names=()
        record_types=()
        parse_failed=0
        while IFS=$'\t' read -r name type extra; do
            [[ -z "${extra}" && -n "${name}" && -n "${type}" ]] || { parse_failed=1; continue; }
            [[ "${type}" == NS || "${type}" == SOA ]] || { record_names+=("${name}"); record_types+=("${type}"); }
        done <<<"${rows}"
        if ((parse_failed)); then
            overall_failed=1
            failed=$((failed + 1))
            continue
        fi
        zone_failed=0
        if ((${#record_names[@]})); then
            for i in "${!record_names[@]}"; do
                # Record deletions are not counted separately: the class
                # summary tracks top-level resources (zones).
                try_delete gcloud dns record-sets delete "${record_names[i]}" --zone="${zone}" --project="${MDB_GKE_PROJECT}" --type="${record_types[i]}" -q || { overall_failed=1; zone_failed=1; }
            done
        fi
        if ((zone_failed == 0)); then
            if try_delete gcloud dns managed-zones delete "${zone}" --project="${MDB_GKE_PROJECT}" -q; then
                deleted=$((deleted + 1))
            else
                failed=$((failed + 1))
                overall_failed=1
            fi
        fi
    done <<<"${zones}"
    class_summary
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
# requiring at least one user-managed key and all keys being old. Denylisted
# accounts are never touched (SERVICE_ACCOUNT_DENYLIST).
# Known gap: an account with NO user-managed keys has no age signal and is
# skipped forever (an interrupted ra-09 run can leave one behind — SA and key
# are created in separate steps). Deleting keyless accounts blindly could hit
# a test mid-setup, so they are left for manual cleanup.
reap_service_accounts() {
    local raw email key_rows rc denylisted=0
    class_start service-accounts
    if ! raw=$(gcloud iam service-accounts list --project="${MDB_GKE_PROJECT}" --format='value(email)'); then
        echo "ERROR: failed to list service-accounts; no deletions for this class" >&2
        overall_failed=1
        class_summary
        return 0
    fi
    if [[ -z "${raw}" ]]; then
        class_summary
        return 0
    fi
    listed=$(count_rows "${raw}")
    while IFS= read -r email; do
        if denylist_matches "${email}" ${SERVICE_ACCOUNT_DENYLIST[@]+"${SERVICE_ACCOUNT_DENYLIST[@]}"}; then
            denylisted=$((denylisted + 1))
            continue
        fi
        if ! key_rows=$(gcloud iam service-accounts keys list --iam-account="${email}" --project="${MDB_GKE_PROJECT}" --managed-by=user --format="value(validAfterTime.date('%s'))"); then
            overall_failed=1
            failed=$((failed + 1))
            continue
        fi
        rc=0
        sa_keys_all_old <<<"${key_rows}" || rc=$?
        if ((rc == 2)); then
            overall_failed=1
            failed=$((failed + 1))
            continue
        fi
        ((rc == 1)) && continue
        old=$((old + 1))
        if try_delete gcloud projects remove-iam-policy-binding "${MDB_GKE_PROJECT}" --member="serviceAccount:${email}" --role=roles/dns.admin -q; then
            if try_delete gcloud iam service-accounts delete "${email}" --project="${MDB_GKE_PROJECT}" -q; then
                deleted=$((deleted + 1))
            else
                failed=$((failed + 1))
                overall_failed=1
            fi
        else
            failed=$((failed + 1))
            overall_failed=1
        fi
    done <<<"${raw}"
    if ((denylisted > 0)); then
        printf '  skipped %s denylisted account(s)\n' "${denylisted}" >&2
    fi
    class_summary
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
