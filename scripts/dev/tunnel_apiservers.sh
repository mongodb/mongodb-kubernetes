#!/bin/bash

set -eou pipefail

if [[ ! -f $1 ]]; then
  echo "usage $0 <path-to-kubeconfig> <host>"
  exit 1
fi

