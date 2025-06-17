#!/usr/bin/env bash

# This file converts release.json values to flat evergreen_expansions.yaml file
# to be source in evergreen's expansion.update function.
#
# Important: expansion.update can only source yaml file as simple key:value lines.

yaml_file=evergreen_expansions.yaml

MONGODB_OPERATOR_VERSION=$(./scripts/release/calculate_next_version.py --initial_version "1.1.0" --initial_commit_sha "13431e8fa6ae9ac8476034093e18c7da162fbd45")

# add additional variables when needed
cat <<EOF >"${yaml_file}"
mongodbOperator: $MONGODB_OPERATOR_VERSION
EOF

echo "Generated ${yaml_file} file from release.json"
cat "${yaml_file}"
