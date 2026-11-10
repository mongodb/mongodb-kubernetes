#!/usr/bin/env bash

set -eou pipefail

test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

script_name=$(readlink -f "${BASH_SOURCE[0]}")
# we reuse script_dir if already set by the script that is sourcing this file
script_dir=${script_dir:-$(dirname "${script_name}")}

# script_dir will be set from the file that is sourcing this file
log_file="${RUN_LOG_FILE:-$(basename "${script_dir}").run.log}"

snippets_src_dir="code_snippets"
snippets_run_dir=".generated"

DEBUG=${DEBUG:-"false"}

_SNIPPETS_OUTPUT_DIR=${_SNIPPETS_OUTPUT_DIR:-"output"}

function snippets_list() {
  src_dir=$1
  # shellcheck disable=SC2012
  ls -1 "${src_dir}" | sort -t '_' -k1,1n -k2,2
}

function run_cleanup() {
  script_file=$1
  rm -rf "${snippets_run_dir}" 2>/dev/null || true
  rm -rf "log" 2>/dev/null || true
  rm -rf ".generated" 2>/dev/null || true
  git restore --staged --worktree rm -rf "${_SNIPPETS_OUTPUT_DIR}" 2>/dev/null || true
  rm -rf "${script_file}.run.log" 2>/dev/null || true
}

function prepare_snippets() {
  echo "Generating code snippets in ${snippets_run_dir}..."

  touch "${log_file}"
  mkdir log 2>/dev/null || true
  mkdir "${_SNIPPETS_OUTPUT_DIR}" 2>/dev/null || true

  rm -rf "${snippets_run_dir}" 2>/dev/null || true
  mkdir "${snippets_run_dir}" 2>/dev/null || true

  file_list=$(snippets_list "${snippets_src_dir}")
  while IFS= read -r file_name; do
    file_path="${snippets_run_dir}/${file_name}"
    (
      echo "# This file is generated automatically from ${snippets_src_dir}/${file_name}"
      echo "# DO NOT EDIT"
      echo "function ${file_name%.sh}() {"
      cat "${snippets_src_dir}/${file_name}"
      echo "}"
    ) > "${file_path}"
  done <<< "${file_list}"
}

# Evergreen reads logs/snippets.junit.suite with gotest.parse_files and shows
# each snippet in the test UI. Only the runner writes to the file, so no snippet
# output can reach the parser. Failure output stays in the task log.
snippets_junit_dir=""
snippets_junit_classname=""
snippets_junit_disabled="false"

function snippets_junit_init() {
  if [[ -n "${snippets_junit_dir}" ]]; then
    return 0
  fi

  local repo_root
  repo_root=$(cd "$(dirname "${script_name}")/../.." && pwd)

  snippets_junit_dir="${SNIPPETS_JUNIT_DIR:-${repo_root}/logs}"
  snippets_junit_classname="${script_dir#"${repo_root}/"}"

  if ! mkdir -p "${snippets_junit_dir}" 2>/dev/null; then
    echo "Warning: cannot create snippet result directory ${snippets_junit_dir}" >&2
    snippets_junit_disabled="true"
  fi
}

function snippets_junit_write_case() {
  local cmd=$1
  local ret=$2
  local duration=$3

  if [[ "${snippets_junit_disabled}" == "true" ]]; then
    return 0
  fi

  snippets_junit_init

  if [[ "${snippets_junit_disabled}" == "true" ]]; then
    return 0
  fi

  local name="${snippets_junit_classname}/${cmd}.sh"
  local status="PASS"
  if [[ ${ret} -ne 0 ]]; then
    status="FAIL"
  fi

  if ! printf '=== RUN   %s\n--- %s: %s (%ss)\n' \
    "${name}" "${status}" "${name}" "${duration}" \
    >>"${snippets_junit_dir}/snippets.junit.suite"; then
    echo "Warning: cannot write snippet result for ${cmd}" >&2
  fi
}

function run() {
  # shellcheck disable=SC1090
  source "${snippets_run_dir}/$1"
  cmd=${1%.sh}

  if grep -q "^${cmd}$" "${log_file}"; then
    # The first execution already wrote the testcase. A skipped call adds none.
    echo "Skipping ${cmd} as it is already executed."
    return 0
  fi

  echo "$(date +"%Y-%m-%d %H:%M:%S") Executing ${cmd}"

  stdout_file="log/${cmd}.stdout.log"
  stderr_file="log/${cmd}.stderr.log"
  local start_time=${SECONDS}
  set +e
  (set -e; set -x; "${cmd}" >"${stdout_file}" 2>"${stderr_file}")
  ret=$?
  set -e
  local duration=$((SECONDS - start_time))
  if [[ ${ret} == 0 ]]; then
    echo "${cmd}" >> "${log_file}"
  else
    echo "Error running: ${cmd}"
  fi

  if [[ ${DEBUG} == "true" || ${ret} != 0 ]]; then
    cat "${stdout_file}"
    cat "${stderr_file}"
  fi

  snippets_junit_write_case "${cmd}" "${ret}" "${duration}"

  return ${ret}
}

function run_for_output() {
  # shellcheck disable=SC1090
  source "${snippets_run_dir}/$1"
  cmd=${1%.sh}

  if grep -q "^${cmd}$" "${log_file}"; then
    # The first execution already wrote the testcase. A skipped call adds none.
    echo "Skipping ${cmd} as it is already executed."
    return 0
  fi

  echo "$(date +"%Y-%m-%d %H:%M:%S") Executing ${cmd}"
  stdout_file="log/${cmd}.stdout.log"
  stderr_file="log/${cmd}.stderr.log"
  local start_time=${SECONDS}
  set +e
  (set -e; set -x; "${cmd}" >"${stdout_file}" 2>"${stderr_file}")
  ret=$?
  set -e
  local duration=$((SECONDS - start_time))
  if [[ ${ret} == 0 ]]; then
    tee "${_SNIPPETS_OUTPUT_DIR}/${cmd}.out" < "${stdout_file}"
  else
    echo "Error running: ${cmd}"
  fi

  if [[ ${ret} == 0 ]]; then
    echo "${cmd}" >> "${log_file}"
  fi

  if [[ ${DEBUG} == "true" || ${ret} != 0 ]]; then
      cat "${stdout_file}"
      cat "${stderr_file}"
  fi

  snippets_junit_write_case "${cmd}" "${ret}" "${duration}"

  return ${ret}
}
