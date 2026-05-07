#!/usr/bin/env bash
#
# Run bash integration tests written in bats.
#
# Discovers all *.bats files under scripts/test/bash/ and runs them.
# Optional args narrow execution to specific files or directories.
#
# Usage:
#   scripts/test/bash/run.sh                 # run all suites
#   scripts/test/bash/run.sh worktree        # run a specific suite directory
#   scripts/test/bash/run.sh path/to/x.bats  # run a single file
#

set -Eeou pipefail

# bats-core is pure bash — clone once into a per-user cache dir if it isn't on
# PATH already. This keeps the suite runnable on hosts that don't have bats
# pre-installed (CI runners, fresh dev machines).
BATS_VERSION="${BATS_VERSION:-v1.13.0}"
BATS_CACHE_DIR="${HOME}/.cache/bats-core"
BATS_BIN="${BATS_CACHE_DIR}/bin/bats"

ensure_bats() {
    if command -v bats >/dev/null 2>&1; then
        BATS_BIN="$(command -v bats)"
        return
    fi
    if [[ -x "${BATS_BIN}" ]]; then
        return
    fi
    echo "bats not found, cloning bats-core ${BATS_VERSION} into ${BATS_CACHE_DIR}..." >&2
    rm -rf "${BATS_CACHE_DIR}"
    if ! git clone --depth 1 --branch "${BATS_VERSION}" \
            https://github.com/bats-core/bats-core.git "${BATS_CACHE_DIR}" >&2; then
        cat >&2 <<EOF
Error: failed to install bats-core.

Install manually with:
  macOS:  brew install bats-core
  Linux:  apt-get install bats   (or set BATS_VERSION + retry this script)
EOF
        exit 1
    fi
}

tests_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "${tests_dir}/../../.." && pwd)"
cd "${project_dir}"

# Make $workdir and other expansion-style env vars available to the bats
# subshells. The bats tests invoke `make switch` inside fresh worktrees,
# which sources scripts/dev/contexts/private-context — that script
# references $workdir under `set -u`. Without sourcing here every test that
# triggers `make switch` fails with "workdir: unbound variable".
# Note: set_env_context.sh redefines $script_dir, so any local of that name
# inherited from this script would be overwritten — keep our names distinct.
if [[ -f .generated/context.export.env && -f scripts/dev/set_env_context.sh ]]; then
    # shellcheck disable=SC1091
    source scripts/dev/set_env_context.sh
fi

ensure_bats

# Resolve args to absolute paths under tests_dir, then collect *.bats files.
declare -a roots=()
if [[ $# -eq 0 ]]; then
    roots=("${tests_dir}")
else
    for arg in "$@"; do
        if [[ -e "${arg}" ]]; then
            roots+=("$(cd "$(dirname "${arg}")" && pwd)/$(basename "${arg}")")
        elif [[ -e "${tests_dir}/${arg}" ]]; then
            roots+=("${tests_dir}/${arg}")
        else
            echo "Error: '${arg}' not found (looked in cwd and ${tests_dir})" >&2
            exit 1
        fi
    done
fi

declare -a test_files=()
for root in "${roots[@]}"; do
    if [[ -f "${root}" ]]; then
        test_files+=("${root}")
    else
        while IFS= read -r f; do
            test_files+=("${f}")
        done < <(find "${root}" -name '*.bats' -type f | sort)
    fi
done

if [[ ${#test_files[@]} -eq 0 ]]; then
    echo "No .bats test files found."
    exit 0
fi

echo "Running ${#test_files[@]} bats test file(s):"
printf '  %s\n' "${test_files[@]}"
echo

exec "${BATS_BIN}" "${test_files[@]}"
