#!/usr/bin/env bash

# This file converts release.json values to flat evergreen_expansions.yaml file
# to be source in evergreen's expansion.update function.
#
# Important: expansion.update can only source yaml file as simple key:value lines.

yaml_file=evergreen_expansions.yaml

# add additional variables when needed
mongodb_operator_from_release=$(jq -r .mongodbOperator < release.json)
cat <<EOF >"${yaml_file}"
mongodbOperator: ${OPERATOR_VERSION:-${mongodb_operator_from_release}}
EOF

echo "Generated ${yaml_file} file from release.json"
cat "${yaml_file}"

