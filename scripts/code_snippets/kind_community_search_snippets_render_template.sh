#!/usr/bin/env bash

set -eou pipefail
source scripts/dev/set_env_context.sh

test_dir="docs/community-search/quick-start"
python scripts/code_snippets/render_template.py "${test_dir}/README.md.j2" "${test_dir}/README.md"
