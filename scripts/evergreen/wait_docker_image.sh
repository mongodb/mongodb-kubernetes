#!/usr/bin/env bash
#

building_namespace="construction-site"
query="podbuilderid in (${label})"

echo "Waiting for label '${query}' to finish"
all_finished="false"

while [[ $all_finished == "false" ]]; do
    for status in $(kubectl -n "${building_namespace}" get pods -l "${query}" -o jsonpath='{.items[*].status.phase}'); do
        if [ "$status" != "Succeeded" ]; then
            sleep 3
            break
        fi
    done
    all_finished="true"
done
