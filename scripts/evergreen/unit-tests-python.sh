#!/usr/bin/env bash

set -Eeou pipefail

# e2e tests are also in python and we will need to ignore them as they are in the docker/mongodb-kubernetes-tests folder
# we also ignore generate_ssdlc_report_test.py as it's very slow
scripts/evergreen/run_python.sh -m pytest --junitxml=python-unit-result.xml --ignore=docker/mongodb-kubernetes-tests --ignore=generate_ssdlc_report_test.py
# additionally, we have one lib which we want to test which is in the =docker/mongodb-kubernetes-tests folder.
scripts/evergreen/run_python.sh -m pytest --junitxml=python-kubeobject-result.xml docker/mongodb-kubernetes-tests/kubeobject

