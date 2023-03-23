#!/usr/bin/env bash

cluster_name=$1
if [[ -z ${cluster_name} ]]; then
  echo "Usage: recreate_kind_cluster.sh <cluster_name>"
  exit 1
fi

scripts/dev/setup_kind_cluster.sh -r -e -n "${cluster_name}" -l "172.18.255.200-172.18.255.250"
