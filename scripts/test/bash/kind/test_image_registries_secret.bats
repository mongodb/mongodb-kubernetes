#!/usr/bin/env bats
# create_image_registries_secret (scripts/funcs/kubernetes) copies the docker config of the host
# into the pull secret of the namespace. `kubectl` is a stub that logs its argv and the manifest
# it is given, so no cluster is needed.

load ../lib/fake_repo

setup() {
  make_fake_repo
  APPLIED_LOG="${BATS_TEST_TMPDIR}/applied.log"
  : >"${APPLIED_LOG}"
  mkdir -p "${FAKE_HOME}/.docker"
  printf '{"auths":{"registry.example":{"auth":"first-token"}}}\n' >"${FAKE_HOME}/.docker/config.json"

  cat >"${STUB_BIN}/kubectl" <<'STUB'
#!/usr/bin/env bash
echo "kubectl $*" >>"${CALL_LOG}"
case "$*" in
  *"get secret"*) [[ "${KUBECTL_STUB_SECRET_EXISTS:-0}" == "1" ]] ;;
  *"config current-context"*) echo "kind-test" ;;
  *"apply -f -"*) cat >>"${APPLIED_LOG}" ;;
esac
STUB
  # A host with podman would take the podman branch of the function.
  printf '#!/usr/bin/env bash\nexit 1\n' >"${STUB_BIN}/podman"
  chmod +x "${STUB_BIN}/kubectl" "${STUB_BIN}/podman"
}

# Runs the function in a fresh bash so the `set -Eeou pipefail` of scripts/funcs/kubernetes
# does not leak into the bats process. Arguments are VAR=value assignments for that process.
run_create_image_registries_secret() {
  run --separate-stderr env PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" APPLIED_LOG="${APPLIED_LOG}" \
    HOME="${FAKE_HOME}" NAMESPACE=ns WATCH_NAMESPACE=ns "$@" bash -c '
    cd "'"${FAKE_REPO}"'"
    source scripts/funcs/kubernetes
    create_image_registries_secret
  '
}

applied_docker_config() {
  grep '\.dockerconfigjson:' "${APPLIED_LOG}" | awk '{print $2}' | base64 --decode
}

@test "create_image_registries_secret: creates the secret when the namespace has none" {
  run_create_image_registries_secret

  assert_succeeded
  grep -qx 'kubectl --context kind-test apply -f -' "${CALL_LOG}"
  grep -q 'name: image-registries-secret' "${APPLIED_LOG}"
}

@test "create_image_registries_secret: an existing secret is applied again with the token of the current docker config" {
  printf '{"auths":{"registry.example":{"auth":"refreshed-token"}}}\n' >"${FAKE_HOME}/.docker/config.json"

  run_create_image_registries_secret KUBECTL_STUB_SECRET_EXISTS=1

  assert_succeeded
  [ "$(calls_matching '^kubectl --context kind-test apply -f -$')" -eq 1 ]
  [[ "$(applied_docker_config)" == *"refreshed-token"* ]]
}
