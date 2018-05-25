#!/bin/bash -e

# if [ ! -d ./vendor ]; then
#     echo "There's no vendor directory. Did you run 'dep ensure'?"
#     exit
# fi

if [ ! -d ./vendor/k8s.io/code-generator ]; then
    git clone https://github.com/kubernetes/code-generator.git vendor/k8s.io/code-generator
fi

scriptdir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

cd ${scriptdir}/vendor/k8s.io/code-generator && ./generate-groups.sh \
  all \
  github.com/10gen/ops-manager-kubernetes/pkg/client \
  github.com/10gen/ops-manager-kubernetes/pkg/apis \
  "mongodb.com:v1beta1 " \
