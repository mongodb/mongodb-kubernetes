#!/usr/bin/env bats
# ecr_login_for_kind_node_image (scripts/funcs/kubernetes), shared by
# recreate_kind_cluster.sh and recreate_kind_clusters.sh. `aws` and `docker`
# are stubs on PATH that log their argv, so no registry or daemon is needed.

load ../lib/fake_repo

ECR_IMAGE="123456789012.dkr.ecr.eu-west-1.amazonaws.com/kind/node:v1.33"

setup() {
  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/../../../.." && pwd)"
  STUB_BIN="${BATS_TEST_TMPDIR}/bin"
  CALL_LOG="${BATS_TEST_TMPDIR}/calls.log"
  ENV_LOG="${BATS_TEST_TMPDIR}/env.log"
  FAKE_HOME="${BATS_TEST_TMPDIR}/home"
  FAKE_PROJECT="${BATS_TEST_TMPDIR}/project"
  mkdir -p "${STUB_BIN}" "${FAKE_HOME}" "${FAKE_PROJECT}"
  : >"${CALL_LOG}"
  : >"${ENV_LOG}"

  cat >"${STUB_BIN}/aws" <<'STUB'
#!/usr/bin/env bash
echo "aws $*" >>"${CALL_LOG}"
[[ "${AWS_STUB_FAIL:-0}" == "1" ]] && exit 1
# Like the real CLI, keys from the environment win over the profile.
[[ "${AWS_ACCESS_KEY_ID:-}" == "undefined" ]] && exit 1
echo "fake-password"
STUB
  # ENV_LOG records the docker environment each call ran with, apart from the argv in CALL_LOG.
  cat >"${STUB_BIN}/docker" <<'STUB'
#!/usr/bin/env bash
echo "docker $*" >>"${CALL_LOG}"
echo "$1 DOCKER_CONFIG=${DOCKER_CONFIG:-} DOCKER_HOST=${DOCKER_HOST:-}" >>"${ENV_LOG}"
case "$1" in
  image) [[ "${DOCKER_STUB_IMAGE_CACHED:-0}" == "1" ]] ;;
  login) cat >/dev/null ;;
  context) echo "unix:///x.sock" ;;
  pull) [[ "${DOCKER_STUB_PULL_FAILS:-0}" != "1" ]] ;;
esac
STUB
  chmod +x "${STUB_BIN}/aws" "${STUB_BIN}/docker"
}

# Runs the function in a fresh bash so the `set -Eeou pipefail` of
# scripts/funcs/kubernetes does not leak into the bats process. Arguments are
# extra VAR=value assignments for that process; stdout lands in $output and
# stderr in $stderr.
run_ecr_login() {
  run --separate-stderr env -u DOCKER_CONFIG -u DOCKER_HOST -u MCK_DEVC_LOCAL_KIND \
    PATH="${STUB_BIN}:${PATH}" CALL_LOG="${CALL_LOG}" ENV_LOG="${ENV_LOG}" \
    HOME="${FAKE_HOME}" PROJECT_DIR="${FAKE_PROJECT}" "$@" bash -c '
    cd "'"${REPO_ROOT}"'"
    source scripts/funcs/kubernetes
    ecr_login_for_kind_node_image
  '
}

@test "ecr_login: non-ECR node image makes no aws or docker login call" {
  run_ecr_login KIND_NODE_IMAGE="ghcr.io/x/y:v1"

  [ "$status" -eq 0 ]
  [ "$(calls_matching '^aws ')" -eq 0 ]
  [ "$(calls_matching '^docker login')" -eq 0 ]
}

@test "ecr_login: cached ECR image skips the login" {
  run_ecr_login KIND_NODE_IMAGE="${ECR_IMAGE}" DOCKER_STUB_IMAGE_CACHED=1

  [ "$status" -eq 0 ]
  [[ "$output" == *"already present locally"* ]]
  [ "$(calls_matching '^aws ')" -eq 0 ]
  [ "$(calls_matching '^docker login')" -eq 0 ]
}

@test "ecr_login: uncached ECR image logs in with the registry and region of the image" {
  run_ecr_login KIND_NODE_IMAGE="${ECR_IMAGE}"

  [ "$status" -eq 0 ]
  [ "$(calls_matching '^aws ecr get-login-password --region eu-west-1$')" -eq 1 ]
  [ "$(calls_matching '^docker login --username AWS --password-stdin 123456789012.dkr.ecr.eu-west-1.amazonaws.com$')" -eq 1 ]
}

@test "ecr_login: unscoped login uses the default docker config and does not pull" {
  run_ecr_login KIND_NODE_IMAGE="${ECR_IMAGE}"

  [ "$status" -eq 0 ]
  grep -qx 'login DOCKER_CONFIG= DOCKER_HOST=' "${ENV_LOG}"
  [ "$(calls_matching '^docker pull')" -eq 0 ]
}

@test "ecr_login: scoped login and pull use a private docker config in the checkout and leave ~/.docker alone" {
  run_ecr_login MCK_DEVC_LOCAL_KIND=1 KIND_NODE_IMAGE="${ECR_IMAGE}"

  [ "$status" -eq 0 ]
  local private="DOCKER_CONFIG=${FAKE_PROJECT}/.generated/cache/docker-login DOCKER_HOST=unix:///x.sock"
  grep -qx "login ${private}" "${ENV_LOG}"
  grep -qx "pull ${private}" "${ENV_LOG}"
  [ "$(calls_matching "^docker pull ${ECR_IMAGE}\$")" -eq 1 ]
  [ -d "${FAKE_PROJECT}/.generated/cache/docker-login" ]
  [ ! -e "${FAKE_HOME}/.docker" ]
}

@test "ecr_login: scoped mode with a cached image makes no login and no pull" {
  run_ecr_login MCK_DEVC_LOCAL_KIND=1 KIND_NODE_IMAGE="${ECR_IMAGE}" DOCKER_STUB_IMAGE_CACHED=1

  [ "$status" -eq 0 ]
  [ "$(calls_matching '^docker login')" -eq 0 ]
  [ "$(calls_matching '^docker pull')" -eq 0 ]
}

@test "ecr_login: a failing scoped pull warns and returns 0" {
  run_ecr_login MCK_DEVC_LOCAL_KIND=1 KIND_NODE_IMAGE="${ECR_IMAGE}" DOCKER_STUB_PULL_FAILS=1

  [ "$status" -eq 0 ]
  [[ "${stderr}" == *"pulling ${ECR_IMAGE} failed"* ]]
}

@test "ecr_login: a failing aws call warns on stderr and still returns 0" {
  run_ecr_login KIND_NODE_IMAGE="${ECR_IMAGE}" AWS_STUB_FAIL=1

  [ "$status" -eq 0 ]
  [[ "$stderr" == *"WARN: ECR login failed"* ]]
}

@test "ecr_login: AWS keys set to undefined are dropped so aws can use the profile" {
  run_ecr_login KIND_NODE_IMAGE="${ECR_IMAGE}" AWS_ACCESS_KEY_ID=undefined AWS_SECRET_ACCESS_KEY=undefined

  [ "$status" -eq 0 ]
  [[ "$stderr" != *"WARN"* ]]
  [ "$(calls_matching '^docker login')" -eq 1 ]
}
