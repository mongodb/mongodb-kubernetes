#!/usr/bin/env bash

set -eou pipefail

test_dir="$1"
scripts/dev/run_python.sh scripts/code_snippets/render_template.py "${test_dir}/README.md.j2" "${test_dir}/README.md"
