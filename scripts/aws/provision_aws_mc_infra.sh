#!/usr/bin/env bash
#
# Provision (or re-provision) the four-EKS-cluster AWS multi-cluster perf/e2e
# infrastructure in eu-south-1, regenerate the merged kubeconfig, and verify node
# reachability. This is SHORT-LIVED infra — tear it down with
# `mongot_multicluster-infra/scripts/enterprise/multicluster.py down`.
#
# Pairs with scripts/dev/contexts/e2e_aws_simulated_mc_sharded (which consumes the
# kubeconfig this script writes). The actual AWS resource provisioning lives in the
# mongot_multicluster-infra repo's hand-rolled boto3 orchestrator (multicluster.py).
#
# Prereqs:
#   - AWS profile `mck-admin` (IAM user, acct 268558157000) with a valid session.
#   - On the corp VPN: EKS API endpoints are corp-prefix-locked, and the in-cluster
#     provisioning steps (lbcontroller/certmanager/storageclasses/nvme) use kubectl/helm.
#   - aws, kubectl, helm on PATH; a python venv with boto3/click/pyyaml/requests.
#
# Usage:
#   scripts/aws/provision_aws_mc_infra.sh [PHASE ...]
#     (no args)            -> runs the default pipeline: reset up apiaccess kubeconfig verify
#     all                  -> same as no args
#     full                 -> default pipeline + e2e-prep (tokens + bootstrap)
#     <phase> [<phase>...] -> run only the named phase(s), in the given order
#   Phases: preflight reset up cpsubnets apiaccess kubeconfig verify tokens bootstrap
#
# Env overrides:
#   INFRA_REPO   (default: ~/mdb/mongot_multicluster-infra)
#   PY           (default: <this repo>/venv/bin/python3)
#   AWS_PROFILE  (default: mck-admin)
#   REGION       (default: eu-south-1)
#   KUBECONFIG_OUT (default: $INFRA_REPO/tmp/search-onprem.kubeconfig)
#   CONCURRENCY  (default: 4)

set -Eeuo pipefail

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------
THIS_REPO="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
INFRA_REPO="${INFRA_REPO:-${HOME}/mdb/mongot_multicluster-infra}"
ENT_DIR="${INFRA_REPO}/scripts/enterprise"
PY="${PY:-${THIS_REPO}/venv/bin/python3}"
export AWS_PROFILE="${AWS_PROFILE:-mck-admin}"
REGION="${REGION:-eu-south-1}"
KUBECONFIG_OUT="${KUBECONFIG_OUT:-${INFRA_REPO}/tmp/search-onprem.kubeconfig}"
CONCURRENCY="${CONCURRENCY:-4}"

# Manifests (one per cluster). Filenames are relative to ENT_DIR.
MANIFESTS=(
  manifest-mc-om-mdb-az1.yaml
  manifest-mc-mdb-az2.yaml
  manifest-mc-search-az1.yaml
  manifest-mc-search-az2.yaml
)
CENTRAL_CONTEXT="om-mdb-az1"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()   { printf '\033[1;32m    ok: %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m    warn: %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

trap 'die "failed at line ${LINENO} (phase: ${CURRENT_PHASE:-?})"' ERR
CURRENT_PHASE=""

# clusterId from a manifest file (e.g. ls-mc-om-mdb-az1)
cluster_id_of() { grep -E '^clusterId:' "${ENT_DIR}/$1" | awk '{print $2}'; }
# short context = clusterId without the ls-mc- prefix (e.g. om-mdb-az1)
short_of()      { local cid; cid="$(cluster_id_of "$1")"; echo "${cid#ls-mc-}"; }
# EKS cluster name = test-cluster-<clusterId>  (lib/manifest.py derivation)
cluster_name_of(){ echo "test-cluster-$(cluster_id_of "$1")"; }

manifest_flags() { local m; for m in "${MANIFESTS[@]}"; do printf -- '--manifest %s ' "$m"; done; }

# ---------------------------------------------------------------------------
# Phases
# ---------------------------------------------------------------------------
phase_preflight() {
  CURRENT_PHASE=preflight; log "preflight"
  [[ -d "${ENT_DIR}" ]] || die "infra repo not found: ${ENT_DIR} (set INFRA_REPO)"
  [[ -x "${PY}" ]] || die "python not found/executable: ${PY} (set PY)"
  "${PY}" -c "import boto3,click,yaml,requests" || die "venv missing boto3/click/pyyaml/requests"
  local t; for t in aws kubectl helm; do command -v "$t" >/dev/null || die "$t not on PATH"; done
  local m; for m in "${MANIFESTS[@]}"; do [[ -f "${ENT_DIR}/$m" ]] || die "manifest missing: ${ENT_DIR}/$m"; done
  local ident
  ident="$(aws sts get-caller-identity --profile "${AWS_PROFILE}" --region "${REGION}" --query 'Arn' --output text)" \
    || die "AWS profile '${AWS_PROFILE}' cannot authenticate (session expired? wrong profile?)"
  ok "AWS identity: ${ident}"
  ok "infra repo: ${ENT_DIR}"
  ok "python: ${PY}"
}

# Remove the provisioner's per-cluster state files. A teardown leaves them STALE
# (lbControllerReady/certManagerReady/apiAllowlistReady=true), which makes `up`
# wrongly SKIP those steps on a fresh deploy. Back them up, then clear.
phase_reset() {
  CURRENT_PHASE=reset; log "reset stale state-*.json"
  local backup; backup="/tmp/mck-aws-mc-state-backup-$(date +%Y%m%d-%H%M%S)"
  if compgen -G "${ENT_DIR}/state-ls-mc-*.json" >/dev/null; then
    mkdir -p "${backup}"
    cp "${ENT_DIR}"/state-ls-mc-*.json "${backup}/" 2>/dev/null || true
    rm -f "${ENT_DIR}"/state-ls-mc-*.json
    ok "cleared state files (backup: ${backup})"
  else
    ok "no state files present (already clean)"
  fi
}

phase_up() {
  CURRENT_PHASE=up; log "provision (multicluster.py up --to certmanager) — EKS control planes + node groups, ~15-25 min"
  # Stop before apiaccess: the dense cluster (om-mdb-az1, 11 nodes) exhausts its
  # private /24 subnet via the VPC CNI warm-IP pool, and apiaccess's
  # UpdateClusterConfig needs >=2 free IPs per AZ. The cpsubnets phase fixes that first.
  ( cd "${ENT_DIR}" && eval "${PY} multicluster.py up $(manifest_flags) --profile ${AWS_PROFILE} --concurrency ${CONCURRENCY} --to certmanager" )
  ok "up (through certmanager) complete"
}

# Ensure every cluster's control-plane subnet set has a subnet with free IPs in each
# AZ, by adding the (lightly-used) public subnet. Without this, apiaccess's
# UpdateClusterConfig fails with "Atleast one subnet in each AZ should have 2 free IPs"
# on the dense cluster whose private subnet the CNI has exhausted. Idempotent.
phase_cpsubnets() {
  CURRENT_PHASE=cpsubnets; log "ensure free-IP control-plane subnet per AZ (pre-apiaccess guard)"
  local m cid cname pub cur
  for m in "${MANIFESTS[@]}"; do
    cid="$(cluster_id_of "${m}")"; cname="test-cluster-${cid}"
    pub="$(aws ec2 describe-subnets --profile "${AWS_PROFILE}" --region "${REGION}" \
      --filters "Name=tag:Name,Values=search-onprem-public-${cid}" \
      --query 'Subnets[0].SubnetId' --output text 2>/dev/null)"
    [[ -n "${pub}" && "${pub}" != "None" ]] || { warn "${cid}: no public subnet found, skipping"; continue; }
    cur="$(aws eks describe-cluster --profile "${AWS_PROFILE}" --region "${REGION}" \
      --name "${cname}" --query 'cluster.resourcesVpcConfig.subnetIds' --output text 2>/dev/null)"
    if grep -qw "${pub}" <<<"${cur}"; then ok "${cid}: public subnet already registered"; continue; fi
    local joined; joined="$(echo "${cur}" | tr '\t' ',')",${pub}
    aws eks update-cluster-config --profile "${AWS_PROFILE}" --region "${REGION}" \
      --name "${cname}" --resources-vpc-config "subnetIds=${joined}" >/dev/null
    printf '    %s: added public subnet %s, waiting ACTIVE' "${cid}" "${pub}"
    local i st
    for i in $(seq 1 40); do
      st="$(aws eks describe-cluster --profile "${AWS_PROFILE}" --region "${REGION}" --name "${cname}" --query 'cluster.status' --output text 2>/dev/null)"
      [[ "${st}" == "ACTIVE" ]] && break
      printf '.'; sleep 20
    done
    echo; ok "${cid}: control-plane subnets updated"
  done
}

# Cross-cluster NAT allowlisting needs ALL clusters' NAT gateways to exist, so on a
# cold start the first apiaccess pass can't see siblings. Re-run it as a final pass.
phase_apiaccess() {
  CURRENT_PHASE=apiaccess; log "second apiaccess pass (cross-cluster NAT allowlist)"
  ( cd "${ENT_DIR}" && eval "${PY} multicluster.py up $(manifest_flags) --profile ${AWS_PROFILE} --from apiaccess" )
  ok "apiaccess complete"
}

# Rebuild the merged kubeconfig: one update-kubeconfig per cluster, aliased to the
# short context name (om-mdb-az1, ...). Auth is exec `aws eks get-token` w/ the profile.
phase_kubeconfig() {
  CURRENT_PHASE=kubeconfig; log "regenerate merged kubeconfig -> ${KUBECONFIG_OUT}"
  mkdir -p "$(dirname "${KUBECONFIG_OUT}")"
  rm -f "${KUBECONFIG_OUT}"
  local m short cname
  for m in "${MANIFESTS[@]}"; do
    short="$(short_of "$m")"; cname="$(cluster_name_of "$m")"
    aws eks update-kubeconfig --name "${cname}" --region "${REGION}" \
      --profile "${AWS_PROFILE}" --alias "${short}" --kubeconfig "${KUBECONFIG_OUT}"
    ok "context ${short} -> ${cname}"
  done
  KUBECONFIG="${KUBECONFIG_OUT}" kubectl config use-context "${CENTRAL_CONTEXT}" >/dev/null
  ok "current-context: ${CENTRAL_CONTEXT}"
}

phase_verify() {
  CURRENT_PHASE=verify; log "verify node reachability (requires corp VPN)"
  local m short n
  for m in "${MANIFESTS[@]}"; do
    short="$(short_of "$m")"
    n="$(KUBECONFIG="${KUBECONFIG_OUT}" kubectl --context "${short}" get nodes --no-headers 2>/dev/null | wc -l | tr -d ' ')"
    if [[ "${n}" -gt 0 ]]; then ok "${short}: ${n} node(s)"; else warn "${short}: 0 nodes reachable (VPN? still joining?)"; fi
  done
}

# --- optional e2e harness prep (not strictly "infra") ---
phase_tokens() {
  CURRENT_PHASE=tokens; log "extract SA bearer tokens (prepare_aws_mc_tokens.sh)"
  # Invoke via bash so a missing +x bit can't break it.
  ( cd "${THIS_REPO}" && source scripts/dev/contexts/e2e_aws_simulated_mc_sharded \
      && AWS_PROFILE="${AWS_PROFILE}" bash scripts/dev/prepare_aws_mc_tokens.sh )
  ok "tokens prepared"
}

phase_bootstrap() {
  CURRENT_PHASE=bootstrap; log "cluster-side bootstrap (bootstrap_aws_mc_e2e.sh)"
  ( cd "${THIS_REPO}" && source scripts/dev/contexts/e2e_aws_simulated_mc_sharded \
      && AWS_PROFILE="${AWS_PROFILE}" bash scripts/dev/bootstrap_aws_mc_e2e.sh )
  ok "bootstrap complete"
}

# ---------------------------------------------------------------------------
# Dispatch
# ---------------------------------------------------------------------------
DEFAULT_PIPELINE=(preflight reset up cpsubnets apiaccess kubeconfig verify)
FULL_PIPELINE=(preflight reset up cpsubnets apiaccess kubeconfig verify tokens bootstrap)

phases=()
if [[ $# -eq 0 || "${1:-}" == "all" ]]; then
  phases=("${DEFAULT_PIPELINE[@]}")
elif [[ "${1:-}" == "full" ]]; then
  phases=("${FULL_PIPELINE[@]}")
else
  phases=("$@")
fi

log "AWS MC infra provisioning — phases: ${phases[*]}"
for p in "${phases[@]}"; do
  case "$p" in
    preflight|reset|up|cpsubnets|apiaccess|kubeconfig|verify|tokens|bootstrap) "phase_${p}" ;;
    *) die "unknown phase: $p" ;;
  esac
done

CURRENT_PHASE=""
log "DONE. KUBECONFIG=${KUBECONFIG_OUT}"
echo "Next: source scripts/dev/contexts/e2e_aws_simulated_mc_sharded && scripts/dev/e2e_aws_simulated_multi_cluster_sharded.sh"
