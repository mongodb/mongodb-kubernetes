#!/usr/bin/env bats

setup() {
  source "$BATS_TEST_DIRNAME/../../../../scripts/dev/contexts/select_quay_credentials.sh"

  quay_staging_username="sentinel-staging-user"
  quay_staging_robot_token="sentinel-staging-token"
  quay_prod_username="sentinel-prod-user"
  quay_prod_robot_token="sentinel-prod-token"
}

run_context_and_assert_preflight_submit() {
  local temp_dir context_path override_value
  temp_dir=$(mktemp -d "$BATS_TEST_TMPDIR/evg-private-context.XXXXXX")
  context_path=$(realpath "$BATS_TEST_DIRNAME/../../../../scripts/dev/contexts/evg-private-context")

  mkdir -p "$temp_dir/scripts/evergreen/e2e" "$temp_dir/scripts/dev/contexts" "$temp_dir/bin"
  printf '{}\n' >"$temp_dir/release.json"
  cat >"$temp_dir/scripts/evergreen/e2e/lib.sh" <<'EOF'
generate_random_namespace() {
  echo test-namespace
}
EOF
  cat >"$temp_dir/scripts/dev/contexts/select_quay_credentials.sh" <<'EOF'
select_quay_credentials() {
  :
}
EOF
  cat >"$temp_dir/bin/jq" <<'EOF'
#!/usr/bin/env bash
case "$2" in
  .mongodbOperator) echo test-operator-version ;;
  .readinessProbeVersion) echo test-readiness-version ;;
  .versionUpgradeHookVersion) echo test-hook-version ;;
  *) exit 1 ;;
esac
EOF
  cat >"$temp_dir/bin/git" <<'EOF'
#!/usr/bin/env bash
echo 12345678
EOF
  chmod +x "$temp_dir/bin/jq" "$temp_dir/bin/git"

  override_value=${2:-__unset__}
  run env -i PATH="$temp_dir/bin:$PATH" HOME="$temp_dir" bash -c '
    context_path=$1
    workdir=$2
    scenario=$3
    override=$4
    branch=$5
    expected=$6

    cd "$workdir"
    script_dir=scripts/dev/contexts
    BUILD_SCENARIO="$scenario"
    branch_name="$branch"
    if [[ "$override" == "__unset__" ]]; then
      unset preflight_submit
    else
      preflight_submit="$override"
    fi

    mms_eng_test_aws_access_key=
    mms_eng_test_aws_secret=
    mms_eng_test_aws_region=
    e2e_cloud_qa_orgid_owner_ubi_cloudqa=
    e2e_cloud_qa_apikey_owner_ubi_cloudqa=
    e2e_cloud_qa_user_owner_ubi_cloudqa=
    EVR_TASK_ID=test-task-id
    community_private_preview_pullsecret_dockerconfigjson=
    cognito_user_pool_id=
    cognito_workload_federation_client_id=
    cognito_user_name=
    cognito_workload_federation_client_secret=
    cognito_user_password=
    cognito_workload_url=
    cognito_workload_user_id=

    source "$context_path"
    [[ "$preflight_submit" == "$expected" ]]
  ' _ "$context_path" "$temp_dir" "$1" "$override_value" "$3" "$4"
  local result=$status
  local output_text=$output
  rm -rf "$temp_dir"

  if [[ $result -ne 0 ]]; then
    echo "$output_text"
    return "$result"
  fi
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

@test "evg private context defaults preflight submit on for release" {
  run_context_and_assert_preflight_submit "release" "__unset__" "release-v1-test" "true"
}

@test "evg private context defaults preflight submit off for dryrun release" {
  run_context_and_assert_preflight_submit "dryrun-release" "__unset__" "master" "false"
}

@test "evg private context preserves explicit false for release" {
  run_context_and_assert_preflight_submit "release" "false" "master" "false"
}

@test "evg private context preserves explicit true for dryrun release" {
  run_context_and_assert_preflight_submit "dryrun-release" "true" "release-v2-test" "true"
}

@test "evg private context defaults preflight submit off outside release" {
  run_context_and_assert_preflight_submit "staging" "__unset__" "master" "false"
}
