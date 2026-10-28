# Shared by the bats suites of the kind scripts in this directory. The scripts run in a
# scratch checkout with stubs for docker, aws, kind, kubectl, istioctl, make and sleep on PATH
# that log their argv to CALL_LOG, so no daemon runs. The helpers the recreate scripts call
# (setup_kind_cluster.sh, interconnect, istio, csi) are stubs too.

load ../lib/fake_repo

recreate_setup() {
  make_fake_repo scripts/dev/recreate_kind_cluster.sh scripts/dev/recreate_kind_clusters.sh
  mkdir -p "${FAKE_REPO}/multi_cluster/tools" "${FAKE_REPO}/istio-1.16.1/bin"
  # What the last `make switch` baked. Like private-context, the context exports
  # DELETE_KIND_NETWORK=true.
  printf 'export PROJECT_DIR=%s\nexport CLUSTER_DOMAIN=cluster.local\nexport DELETE_KIND_NETWORK=true\n' \
    "${FAKE_REPO}" >"${FAKE_REPO}/.generated/context.export.env"

  local helper
  for helper in scripts/dev/setup_kind_cluster.sh scripts/dev/interconnect_kind_clusters.sh \
    multi_cluster/tools/install_istio.sh multi_cluster/tools/install_istio_central.sh; do
    printf '#!/usr/bin/env bash\necho "%s $*" >>"${CALL_LOG}"\n' "${helper}" >"${FAKE_REPO}/${helper}"
    chmod +x "${FAKE_REPO}/${helper}"
  done
  printf '#!/usr/bin/env bash\n: # the istio download is a no-op in these tests\n' \
    >"${FAKE_REPO}/multi_cluster/tools/download_istio.sh"
  printf 'csi_driver_download() { :; }\ncsi_driver_deploy() { echo "csi ${1}" >>"${CALL_LOG}"; }\n' \
    >"${FAKE_REPO}/scripts/dev/install_csi_driver.sh"
  printf '#!/usr/bin/env bash\necho "istioctl $*" >>"${CALL_LOG}"\n' >"${FAKE_REPO}/istio-1.16.1/bin/istioctl"
  chmod +x "${FAKE_REPO}/istio-1.16.1/bin/istioctl"

  stub_logger aws kind kubectl make sleep
  cat >"${STUB_BIN}/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"${CALL_LOG}"
case "$1 $2" in
  # The node image is never cached, so an ECR image always needs a login.
  "image inspect") exit 1 ;;
  "network ls") echo "abc123 kind bridge local" ;;
  "network inspect")
    if [[ "$3" == "-f" ]]; then
      # The format tells the two callers apart: IPAM subnets or the containers.
      case "$4" in
        *IPAM*) echo "${DOCKER_STUB_NET_SUBNET:-}" ;;
        *) [[ -z "${DOCKER_STUB_NET_CONTAINERS:-}" ]] || echo "${DOCKER_STUB_NET_CONTAINERS}" ;;
      esac
    else
      # Absent network unless the test gives it a subnet.
      [[ -n "${DOCKER_STUB_NET_SUBNET:-}" ]] || exit 1
    fi
    ;;
esac
exit 0
STUB
  chmod +x "${STUB_BIN}/docker"
}

# Runs a command in the scratch repo in a fresh bash. Arguments up to `--` are
# VAR=value assignments for that process; the rest is the command. The variables
# under test start unset, whatever the caller's shell exports.
run_in_fake_repo() {
  local assignments=()
  while [[ "$1" != "--" ]]; do assignments+=("$1"); shift; done
  shift
  run --separate-stderr env -u KIND_LB_PREFIX -u KIND_NETWORK_SUBNET \
    PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" HOME="${FAKE_HOME}" \
    KIND_NODE_IMAGE="ghcr.io/example/node:v1" \
    ${assignments[@]+"${assignments[@]}"} bash -c 'cd "'"${FAKE_REPO}"'" && "$@"' _ "$@"
}

# run_in_fake_repo for a command that must exit 0.
run_in_fake_repo_ok() {
  run_in_fake_repo "$@"
  assert_succeeded
}

# Compares the sorted call log with fixtures/argv-<scenario>.log.
assert_golden() {
  local fixture="${BATS_TEST_DIRNAME}/fixtures/argv-$1.log" actual="${BATS_TEST_TMPDIR}/actual.log"
  sed "s#${FAKE_REPO}#<repo>#g" "${CALL_LOG}" | LC_ALL=C sort >"${actual}"
  if [[ "${GOLDEN_RECORD:-}" == 1 ]]; then
    mkdir -p "${BATS_TEST_DIRNAME}/fixtures"
    cp "${actual}" "${fixture}"
    return 0
  fi
  diff -u "${fixture}" "${actual}"
}
