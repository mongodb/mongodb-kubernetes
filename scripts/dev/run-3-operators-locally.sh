#!/usr/bin/env bash
#
# run-3-operators-locally.sh — Phase D D'4.
#
# Bring up three operator processes in the devcontainer, one per member
# cluster, all in distributed mode (RAFT_PEERS set), wired into a single
# Raft cluster over 127.0.0.1:7001-7003. Each operator gets its own
# per-cluster kubeconfig (produced by D'2's extract_member_kubeconfigs.sh)
# and distinct metrics/health ports (per §14.4 D'4 — 8181/2/3, 8191/2/3).
#
# Sessions: tmux sessions mck-op-cluster-1, mck-op-cluster-2, mck-op-cluster-3.
# Logs:     logs/operator-cluster-{1,2,3}.log (stable filenames so the
#           orchestrator script (D'6) can tail them without timestamp lookup).
#
# Usage:
#   scripts/dev/run-3-operators-locally.sh                # start + wait for raft
#   scripts/dev/run-3-operators-locally.sh --stop         # kill all three
#   scripts/dev/run-3-operators-locally.sh --status       # report what's up
#
# Env vars (optional overrides):
#   RAFT_BASE_PORT     base TCP port for raft (default 7001 — uses ports 7001,
#                      7002, 7003).
#   METRICS_BASE_PORT  default 8181 (uses 8181, 8182, 8183).
#   HEALTH_BASE_PORT   default 8191 (uses 8191, 8192, 8193).
#   WATCH_NAMESPACE    namespace the operator restricts watches to.
#                      Defaults to whatever ${NAMESPACE} resolves to in
#                      the devc context (ls-<stack-idx>).
#   READY_TIMEOUT_S    seconds to wait for raft to form (default 90).

set -Eeuo pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

cd "$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)"

# Source the per-side env. devenv loads .generated/context.env and
# .generated/context.devc.env; NAMESPACE / OPERATOR_ENV / WATCH_NAMESPACE
# come from there.
# shellcheck disable=SC1091
. scripts/dev/devenv

# The cluster identity each operator self-reports must match the
# clusterSpecList.clusterName values in the MongoDB CR (kind-e2e-cluster-N
# for this devc setup) so distGateInline can match leases against the
# correct member cluster. The per-cluster kubeconfig filename uses the
# "cluster-N" stem (from extract_member_kubeconfigs.sh, which drops the
# "kind-e2e-" prefix), so we keep the two mappings explicit below.
CLUSTERS=(kind-e2e-cluster-1 kind-e2e-cluster-2 kind-e2e-cluster-3)
KUBECONFIG_STEMS=(cluster-1 cluster-2 cluster-3)
RAFT_BASE_PORT="${RAFT_BASE_PORT:-7001}"
METRICS_BASE_PORT="${METRICS_BASE_PORT:-8181}"
HEALTH_BASE_PORT="${HEALTH_BASE_PORT:-8191}"
WEBHOOK_BASE_PORT="${WEBHOOK_BASE_PORT:-11993}"
READY_TIMEOUT_S="${READY_TIMEOUT_S:-90}"
WATCH_NAMESPACE="${WATCH_NAMESPACE:-${NAMESPACE:-}}"
if [[ -z "${WATCH_NAMESPACE}" ]]; then
  echo "ERROR: WATCH_NAMESPACE is empty and NAMESPACE is unset — re-source devenv after 'make switch'." >&2
  exit 1
fi

mkdir -p logs

build_peers() {
  local out=()
  local i=0
  for c in "${CLUSTERS[@]}"; do
    out+=("${c}=127.0.0.1:$((RAFT_BASE_PORT + i))")
    i=$((i + 1))
  done
  local IFS=,
  echo "${out[*]}"
}

# Helper: kubeconfig filename stem (cluster-1) for a given cluster name
# (kind-e2e-cluster-1). Strips the "kind-e2e-" prefix.
stem_for() {
  echo "${1#kind-e2e-}"
}

session_name() { echo "mck-op-$(stem_for "$1")"; }
log_path() { echo "logs/operator-$(stem_for "$1").log"; }
kubeconfig_path() { echo ".generated/$(stem_for "$1").kubeconfig"; }

start_one() {
  local cluster="$1" idx="$2"
  local raft_port=$((RAFT_BASE_PORT + idx))
  local metrics_port=$((METRICS_BASE_PORT + idx))
  local health_port=$((HEALTH_BASE_PORT + idx))
  local webhook_port=$((WEBHOOK_BASE_PORT + idx))
  local kc; kc="$(kubeconfig_path "${cluster}")"
  local lp; lp="$(log_path "${cluster}")"
  local sn; sn="$(session_name "${cluster}")"

  if [[ ! -f "${kc}" ]]; then
    echo "ERROR: per-cluster kubeconfig missing: ${kc}" >&2
    echo "       Run scripts/dev/extract_member_kubeconfigs.sh first (D'2)." >&2
    return 1
  fi

  local bootstrap=false
  if [[ ${idx} -eq 0 ]]; then bootstrap=true; fi

  local peers
  peers="$(build_peers)"

  # Wipe stale port-holders so a previous unclean exit doesn't crash this.
  for port in "${raft_port}" "${metrics_port}" "${health_port}" "${webhook_port}"; do
    local pid
    pid="$(lsof -ti:"${port}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]]; then
      echo "  killing stale pid ${pid} on port ${port}"
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done

  # Truncate prior log so the wait-for-raft check below doesn't see stale lines.
  : > "${lp}"

  # The operator binary inherits these env vars. Distributed mode is
  # triggered by RAFT_PEERS being non-empty (see main.go D'1 wiring).
  # We pass --watch-resource explicitly so the multicluster controller is
  # registered (and then disabled by the distributed-mode branch in main.go),
  # mirroring how op_run.sh runs for the hub-spoke path.
  local watch_args="--watch-resource=mongodb \
--watch-resource=mongodbusers \
--watch-resource=opsmanagers \
--watch-resource=mongodbsearch \
--watch-resource=clustermongodbroles"

  tmux kill-session -t "${sn}" 2>/dev/null || true
  tmux new-session -d -s "${sn}" "bash -lc 'cd \"$(pwd)\"; \
. scripts/dev/devenv; \
export KUBECONFIG=\"${kc}\"; \
export KUBE_CONFIG_PATH=\"${kc}\"; \
export RAFT_CLUSTER_NAME=\"${cluster}\"; \
export RAFT_BIND_ADDR=\"127.0.0.1:${raft_port}\"; \
export RAFT_PEERS=\"${peers}\"; \
export RAFT_BOOTSTRAP=\"${bootstrap}\"; \
export METRICS_BIND_ADDRESS=\"127.0.0.1:${metrics_port}\"; \
export HEALTH_PROBE_BIND_ADDRESS=\"127.0.0.1:${health_port}\"; \
export MDB_WEBHOOK_PORT=\"${webhook_port}\"; \
export WATCH_NAMESPACE=\"${WATCH_NAMESPACE}\"; \
export NAMESPACE=\"${WATCH_NAMESPACE}\"; \
export CURRENT_NAMESPACE=\"${WATCH_NAMESPACE}\"; \
export OPERATOR_NAME=\"mongodb-kubernetes-operator-${cluster}\"; \
go run ./main.go ${watch_args} 2>&1 | tee ${lp}'"

  echo "[${cluster}] started session=${sn} kubeconfig=${kc} raft=127.0.0.1:${raft_port} bootstrap=${bootstrap}"
}

stop_all() {
  for c in "${CLUSTERS[@]}"; do
    local sn; sn="$(session_name "${c}")"
    if tmux has-session -t "${sn}" 2>/dev/null; then
      echo "stopping ${sn}"
      tmux kill-session -t "${sn}" || true
    fi
  done
  # Also kill any stray go-run processes by port.
  local i=0
  for _ in "${CLUSTERS[@]}"; do
    local raft_port=$((RAFT_BASE_PORT + i))
    local pid
    pid="$(lsof -ti:"${raft_port}" 2>/dev/null || true)"
    if [[ -n "${pid}" ]]; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
    i=$((i + 1))
  done
}

status_all() {
  for c in "${CLUSTERS[@]}"; do
    local sn; sn="$(session_name "${c}")"
    local lp; lp="$(log_path "${c}")"
    local up=no
    if tmux has-session -t "${sn}" 2>/dev/null; then up=yes; fi
    local last_line=""
    if [[ -s "${lp}" ]]; then
      last_line="$(tail -n 1 "${lp}" | head -c 140)"
    fi
    printf "  %-12s session=%s  log=%s  last=%s\n" "${c}" "${up}" "${lp}" "${last_line}"
  done
}

wait_for_raft() {
  local deadline=$((SECONDS + READY_TIMEOUT_S))
  echo "waiting up to ${READY_TIMEOUT_S}s for operators to start + raft ports to listen..."
  while (( SECONDS < deadline )); do
    local n_distributed_on=0
    local n_workers_started=0
    local n_ports_listening=0
    local i=0
    for c in "${CLUSTERS[@]}"; do
      local lp; lp="$(log_path "${c}")"
      local raft_port=$((RAFT_BASE_PORT + i))

      # Hard-fail detection first.
      # - panic: stack-trace.
      # - level=fatal: explicit fatal logger.
      # - distributed mode init: our own pre-manager validation failure.
      # - exit status 1: tail of go-run failure.
      # We deliberately DO NOT match "bind: address already in use" generically
      # because pprof on port 10081 collides between the three processes
      # (every operator wants the same port) and the pprof.go path just logs
      # ERROR + moves on. Manager startup failures show up as panics or as
      # the go-run process exiting with non-zero.
      if [[ -s "${lp}" ]] && grep -qE "panic:|level=fatal|distributed mode init:|^exit status [1-9]" "${lp}"; then
        echo "ERROR: ${c} log shows fatal error:"
        tail -n 30 "${lp}" >&2
        return 1
      fi

      # Soft signal: our own log line "Distributed mode ON: cluster=<name>".
      # The regex must escape "-" inside the name — grep -E treats "-" as
      # literal so no escaping needed; just match the full name.
      if [[ -s "${lp}" ]] && grep -qF "Distributed mode ON: cluster=${c}" "${lp}"; then
        n_distributed_on=$((n_distributed_on + 1))
      fi
      # Soft signal: controller-runtime "Starting workers" — manager fully up.
      if [[ -s "${lp}" ]] && grep -qE "Starting workers.*mongodbshardedcluster-controller" "${lp}"; then
        n_workers_started=$((n_workers_started + 1))
      fi
      # Hard signal: raft port is bound.
      if ss -tln "( sport = :${raft_port} )" 2>/dev/null | grep -q "127.0.0.1:${raft_port}"; then
        n_ports_listening=$((n_ports_listening + 1))
      fi
      i=$((i + 1))
    done

    if (( n_distributed_on == 3 )) && (( n_workers_started == 3 )) && (( n_ports_listening == 3 )); then
      echo "all 3 operators up: distributed_on=${n_distributed_on} workers_started=${n_workers_started} raft_ports_listening=${n_ports_listening}"
      # Small grace period for raft to elect a leader (hashicorp/raft default
      # election timeout is ~150-300ms).
      sleep 2
      return 0
    fi
    sleep 2
  done
  echo "ERROR: operators did NOT reach ready state within ${READY_TIMEOUT_S}s."
  echo "  distributed_on=${n_distributed_on:-0} workers_started=${n_workers_started:-0} raft_ports_listening=${n_ports_listening:-0}"
  for c in "${CLUSTERS[@]}"; do
    echo "--- $(log_path "${c}") (last 40 lines) ---"
    tail -n 40 "$(log_path "${c}")" 2>&1 | sed 's/^/  /'
  done
  return 1
}

apply_member_crds() {
  echo "applying CRDs to each member cluster (idempotent)..."
  local crd_dir="helm_chart/crds"
  if [[ ! -d "${crd_dir}" ]]; then
    echo "ERROR: CRD dir not found: ${crd_dir}" >&2
    return 1
  fi
  for c in "${CLUSTERS[@]}"; do
    local kc; kc="$(kubeconfig_path "${c}")"
    if [[ ! -f "${kc}" ]]; then
      echo "ERROR: per-cluster kubeconfig missing: ${kc} — run extract_member_kubeconfigs.sh first." >&2
      return 1
    fi
    if ! kubectl --kubeconfig "${kc}" apply -f "${crd_dir}" >/dev/null 2>&1; then
      # Try again with verbose output to surface the error.
      echo "ERROR: kubectl apply -f ${crd_dir} failed for ${c}:"
      kubectl --kubeconfig "${kc}" apply -f "${crd_dir}" 2>&1 | sed 's/^/  /'
      return 1
    fi
    echo "  ${c}: CRDs applied"
  done
}

cmd="${1:-start}"
case "${cmd}" in
  --stop|stop)
    stop_all
    ;;
  --status|status)
    status_all
    ;;
  --start|start|"")
    if ! apply_member_crds; then
      echo "ABORT — CRD install failed; see above." >&2
      exit 1
    fi
    echo "starting 3 distributed operators (peers=$(build_peers))..."
    i=0
    for c in "${CLUSTERS[@]}"; do
      start_one "${c}" "${i}"
      i=$((i + 1))
    done
    if ! wait_for_raft; then
      echo "ABORT — see logs above. Run --stop to clean up." >&2
      exit 1
    fi
    echo
    echo "All three operators started. Tail with:"
    for c in "${CLUSTERS[@]}"; do
      echo "  tmux a -t $(session_name "${c}")        # or: tail -F $(log_path "${c}")"
    done
    ;;
  -h|--help)
    sed -n '3,30p' "$0"
    ;;
  *)
    echo "Unknown command: ${cmd}" >&2
    exit 1
    ;;
esac
