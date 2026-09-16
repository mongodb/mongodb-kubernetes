#!/usr/bin/env bats
# e2e_local.sh argument handling. These paths exit before touching wt-ctl or
# any stack, so no EVG/devcontainer resources are needed.

setup() {
  REPO_ROOT="$(cd "${BATS_TEST_DIRNAME}/../../../.." && pwd)"
  SCRIPT="${REPO_ROOT}/scripts/dev/e2e_local.sh"
}

@test "e2e_local: --help prints usage and exits 0" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"One-shot local e2e"* ]]
}

@test "e2e_local: missing --marker exits 2" {
  run "$SCRIPT" --context foo
  [ "$status" -eq 2 ]
  [[ "$output" == *"--marker is required"* ]]
}

@test "e2e_local: missing --context exits 2" {
  run "$SCRIPT" --marker bar
  [ "$status" -eq 2 ]
  [[ "$output" == *"--context is required"* ]]
}

@test "e2e_local: unknown flag exits 2" {
  run "$SCRIPT" --nope
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown argument"* ]]
}
