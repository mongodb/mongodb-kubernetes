#!/bin/bash

set -euo pipefail

helm plugin install https://github.com/helm-unittest/helm-unittest.git --verify=false
