#!/usr/bin/env bash

set -x

# This script updates Go version in the Enterprise Operator repo. The implementation is very naive and
# just massively replaces one to another. Please carefully review the Pull Request.
# This script doesn't validate any parameters.
#
# Usage:
#   ./scripts/dec/update_go.sh 1.23 1.23


prev_ver=$1
next_ver=$2

# Ensure we match dot correctly with \.
prev_ver="${prev_ver/\./\\.}"
next_ver="${next_ver/\./\\.}"

for i in $(git grep --files-with-matches "[go :]${prev_ver}" | grep -v RELEASE_NOTES.md)
do
  perl -p -i -e "s/${prev_ver}/${next_ver}/g" "${i}"
done
