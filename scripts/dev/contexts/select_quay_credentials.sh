#!/usr/bin/env bash
# Selects Quay credentials based on the build scenario.
# Usage: source this file, then call: select_quay_credentials <build-scenario>
# Sets (and exports) quay_username and quay_password.
# Staging and dryrun-release scenarios use staging credentials,
# all other scenarios use production credentials. Unset values default to empty.
select_quay_credentials() {
  local build_scenario="${1:-}"
  if [ "${build_scenario}" == "staging" ] || [ "${build_scenario}" == "dryrun-release" ]; then
    quay_username="${quay_staging_username:-}"
    quay_password="${quay_staging_robot_token:-}"
  else
    quay_username="${quay_prod_username:-}"
    quay_password="${quay_prod_robot_token:-}"
  fi
  export quay_username quay_password
}
