#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

usage() {
  echo "Switch context by passing the test (evergreen task name or full evergreen task URL)."
  echo "If there is more than one variant running given test, then fzf picker is used."
  echo "Usage: $0 <test>"
  echo "  <test> is a task name from .evergreen.yml (e.g. 'e2e_search_community_basic') or a full Evergreen task URL."
}

source scripts/funcs/errors

list_pytest_marks() {
  rg -g '*.py' -o --no-line-number --no-heading --replace '$1' -m 1 \
    '@(?:pytest\.)?mark\.(e2e_[a-zA-Z0-9_]+)' \
    docker/mongodb-kubernetes-tests
}

pick_test_by_file_mark_or_task_url() {
    if ! test_list="$(list_pytest_marks | sort -u)"; then
      echo "Couldn't list pytest marks."
      echo "${test_list}"
      return 1
    fi

    test=$(fzf --print-query --header-first --with-nth '{2}: {1}' -d ':' --accept-nth 2 \
      --header "Select file or task to find contexts where its used. You can paste full task's evergreen url here" <<< "${test_list}") || true
    if [[ -z ${test} ]]; then
      echo "Aborted selecting test"
      return 1
    fi

    # test may contain one or two lines (file:mark or just mark/url)
    number_of_selected_lines=$(wc -l <<< "${test}")
    if [[ ${number_of_selected_lines} -eq 2 ]]; then
      test="$(tail -n 1 <<< "${test}")"
    elif [[ ${number_of_selected_lines} -gt 2 ]]; then
      echo "Too many lines selected: ${test}"
      return 1
    fi

    echo "${test}"
}

main() {
  test="${1:-}"

  if [[ -z ${test} ]]; then
    test=$(pick_test_by_file_mark_or_task_url)
    echo "Selected test: ${test}"
  fi

  if [[ "${test}" = *spruce.mongodb.com* ]]; then
    find_variant_arg="--task-url"
  else
    find_variant_arg="--task-name"
  fi

  if ! contexts=$(scripts/dev/run_python.sh scripts/python/find_test_variants.py "${find_variant_arg}" "${test}"); then
    echo "Couldn't find any test contexts running test: ${test}"
    echo "${contexts}"
    exit 1
  fi

  echo "Found contexts that are running test: ${test}"
  echo "${contexts}"

  selected_context="${contexts}"
  if [[ $(wc -l <<< "${contexts}") -gt 1 ]]; then
    if ! selected_context=$(fzf --header "${test} runs in multiple variants/contexts. Select one to switch context into." --header-first --layout=reverse <<< "${contexts}"); then
      echo "Aborted selecting context"
      exit 1
    fi
  fi

  scripts/dev/switch_context.sh "${selected_context}"
}

main "$@"
