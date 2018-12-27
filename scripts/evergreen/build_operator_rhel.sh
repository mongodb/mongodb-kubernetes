#!/usr/bin/env bash

if [ "$#" -ne 2 ]; then
    printf "Usage:\n"
    printf "\tbuild_operator_rhel.sh <release> <redhat_project_id>\n"
    exit 1
fi

if [ ! -f "release.json" ]; then
    echo "This needs to be run from operator root directory"
    exit 1
fi

release=$1
rh_project_id=$2

build_result=$(\
    curl -X POST "https://connect.redhat.com/api/v2/projects/${rh_project_id}/build" \
         -d '{"tag":"'$release'"}' \
         -H "Content-Type: application/json" \
         -s -o build_status.txt \
         -w "%{http_code}" )

if [ "$build_result" != "201" ]; then
    echo "Error getting RedHat Build Service to build our image. Got status code: $build_result"
    exit 1
fi
