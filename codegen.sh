#!/bin/bash -e

# Copyright 2018 MongoDB Inc. All rights reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

if [ ! -d ./vendor ]; then
    echo "There's no vendor directory. Did you run 'dep ensure'?"
    exit
fi

if [ ! -d ./vendor/k8s.io/code-generator ]; then
    mkdir -p vendor/github.com/kubernetes
    git clone https://github.com/kubernetes/code-generator.git vendor/k8s.io/code-generator
fi

scriptdir="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

cd ${scriptdir}/vendor/k8s.io/code-generator && ./generate-groups.sh \
  all \
  github.com/10gen/ops-manager-kubernetes/pkg/client \
  github.com/10gen/ops-manager-kubernetes/pkg/apis \
  "mongodb.com:v1alpha1" \
