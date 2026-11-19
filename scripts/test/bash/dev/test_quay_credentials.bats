#!/usr/bin/env bats

setup() {
  source "$BATS_TEST_DIRNAME/../../../../scripts/dev/contexts/select_quay_credentials.sh"

  quay_staging_username="sentinel-staging-user"
  quay_staging_robot_token="sentinel-staging-token"
  quay_prod_username="sentinel-prod-user"
  quay_prod_robot_token="sentinel-prod-token"
}

@test "staging uses staging credentials" {
  select_quay_credentials "staging"

  [[ "$quay_username" == "sentinel-staging-user" ]]
  [[ "$quay_password" == "sentinel-staging-token" ]]
}

@test "dryrun-release uses staging credentials" {
  select_quay_credentials "dryrun-release"

  [[ "$quay_username" == "sentinel-staging-user" ]]
  [[ "$quay_password" == "sentinel-staging-token" ]]
}

@test "release uses production credentials" {
  select_quay_credentials "release"

  [[ "$quay_username" == "sentinel-prod-user" ]]
  [[ "$quay_password" == "sentinel-prod-token" ]]
}

@test "patch uses production credentials" {
  select_quay_credentials "patch"

  [[ "$quay_username" == "sentinel-prod-user" ]]
  [[ "$quay_password" == "sentinel-prod-token" ]]
}

@test "unset production values default to empty" {
  unset quay_prod_username quay_prod_robot_token
  select_quay_credentials "release"

  [[ "$quay_username" == "" ]]
  [[ "$quay_password" == "" ]]
}
