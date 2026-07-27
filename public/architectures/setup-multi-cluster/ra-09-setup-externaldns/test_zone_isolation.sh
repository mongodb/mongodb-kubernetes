#!/usr/bin/env bash

set -eou pipefail

script_name=$(readlink -f "${BASH_SOURCE[0]}")
script_dir=$(dirname "${script_name}")

fail=0

# Temp directory for stub kubectl and captured output
tmp_dir=$(mktemp -d)
trap 'rm -rf "${tmp_dir}"' EXIT

# Stub kubectl: writes stdin to KUBECTL_CAPTURE_FILE so we can inspect rendered YAML
cat > "${tmp_dir}/kubectl" <<'STUB'
#!/usr/bin/env bash
cat >> "${KUBECTL_CAPTURE_FILE:-/dev/null}"
STUB
chmod +x "${tmp_dir}/kubectl"

capture_file="${tmp_dir}/rendered.yaml"

# --- Structural checks ---

# 1. YAML must scope external-dns to the current zone via --zone-id-filter
if ! grep -q -- "--zone-id-filter" "${script_dir}/yamls/externaldns.yaml"; then
  echo "FAIL: externaldns.yaml must contain --zone-id-filter to scope to the current zone"
  fail=1
fi

# 2. Delete snippet must set pipefail for pipeline error propagation
if ! grep -q -- "set -o pipefail" "${script_dir}/code_snippets/ra-09_9100_delete_dns_zone.sh"; then
  echo "FAIL: ra-09_9100_delete_dns_zone.sh must set -o pipefail for pipeline error propagation"
  fail=1
fi

# 3. Delete snippet must skip only NS and SOA records
if ! grep -q -- '!= "SOA"' "${script_dir}/code_snippets/ra-09_9100_delete_dns_zone.sh"; then
  echo "FAIL: ra-09_9100_delete_dns_zone.sh must skip only NS and SOA records"
  fail=1
fi

# 4. Teardown must aggregate cleanup status so popd/exit always run
if ! grep -q -- "cleanup_failed" "${script_dir}/teardown.sh"; then
  echo "FAIL: teardown.sh must aggregate cleanup status"
  fail=1
fi

# --- Behavioral checks with stub kubectl ---

# 5. Install snippet must render DNS_ZONE into the YAML output
rm -f "${capture_file}"
if ! (
  cd "${script_dir}" &&
  PATH="${tmp_dir}:${PATH}" \
  DNS_ZONE="test-zone-id" \
  KUBECTL_CAPTURE_FILE="${capture_file}" \
  K8S_CLUSTER_0_CONTEXT_NAME="ctx0" \
  K8S_CLUSTER_1_CONTEXT_NAME="ctx1" \
  K8S_CLUSTER_2_CONTEXT_NAME="ctx2" \
  bash code_snippets/ra-09_0200_install_externaldns.sh
); then
  echo "FAIL: install snippet failed with DNS_ZONE set"
  fail=1
fi

if [[ ! -s "${capture_file}" ]]; then
  echo "FAIL: install snippet produced no rendered output"
  fail=1
elif ! grep -q -- "--zone-id-filter=test-zone-id" "${capture_file}"; then
  echo "FAIL: rendered YAML must contain --zone-id-filter=test-zone-id"
  fail=1
fi

# 6. Install snippet must fail when DNS_ZONE is empty
if (
  cd "${script_dir}" &&
  PATH="${tmp_dir}:${PATH}" \
  DNS_ZONE="" \
  KUBECTL_CAPTURE_FILE="${capture_file}" \
  K8S_CLUSTER_0_CONTEXT_NAME="ctx0" \
  K8S_CLUSTER_1_CONTEXT_NAME="ctx1" \
  K8S_CLUSTER_2_CONTEXT_NAME="ctx2" \
  bash code_snippets/ra-09_0200_install_externaldns.sh
) 2>/dev/null; then
  echo "FAIL: install snippet must fail when DNS_ZONE is empty"
  fail=1
fi

# 7. Install snippet must fail when DNS_ZONE is unset
if (
  cd "${script_dir}" &&
  unset DNS_ZONE &&
  PATH="${tmp_dir}:${PATH}" \
  KUBECTL_CAPTURE_FILE="${capture_file}" \
  K8S_CLUSTER_0_CONTEXT_NAME="ctx0" \
  K8S_CLUSTER_1_CONTEXT_NAME="ctx1" \
  K8S_CLUSTER_2_CONTEXT_NAME="ctx2" \
  bash code_snippets/ra-09_0200_install_externaldns.sh
) 2>/dev/null; then
  echo "FAIL: install snippet must fail when DNS_ZONE is unset"
  fail=1
fi

if [[ ${fail} -ne 0 ]]; then
  exit 1
fi

echo "Zone isolation regression test PASSED"
