#!/usr/bin/env bash

cd "$1" || exit 0

removable_file_patterns="windows win32 osx rpm deb s390x suse12 ubuntu1404 amzn64 ppc64le"

for pattern in $removable_file_patterns; do
    find . -name "*${pattern}*"
    find . -name "*${pattern}*" | xargs rm &> /dev/null
done
