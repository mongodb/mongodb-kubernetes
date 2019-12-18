#!/usr/bin/env bash
#

building_namespace="construction-site"

# Splits a string on ", ", and slices each element up to 63 chars long.
# Returns the array as coma separated values.
function split_and_slice {
    IFS=", "
    read -ra splitted <<< "${1}"

    declare -a sliced
    for i in "${!splitted[@]}"; do
        sliced[$i]="${splitted[$i]:0:63}"
    done

    echo "${sliced[*]}" # equivalent to ",".join(sliced)
}

labels=$(split_and_slice "${label}")
query="podbuilderid in (${labels})"

echo "Waiting for label '${query}' to finish"
all_finished="false"

while [[ $all_finished == "false" ]]; do
    all_finished="true"
    for status in $(kubectl -n "${building_namespace}" get pods -l "${query}" -o jsonpath='{.items[*].status.phase}'); do
        if [ "$status" != "Succeeded" ]; then
            sleep 3
            all_finished="false"
            break  # retry as soon as we have first non succeeding Pod
        fi
    done
done
