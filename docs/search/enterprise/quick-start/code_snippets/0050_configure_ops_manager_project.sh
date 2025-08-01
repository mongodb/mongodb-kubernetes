if [[ "${ops_manager_version}" == "cloud_qa" && -n "${PROJECT_DIR}" ]]; then
  pushd "${PROJECT_DIR}"
  scripts/dev/configure_operator.sh
  popd
fi
