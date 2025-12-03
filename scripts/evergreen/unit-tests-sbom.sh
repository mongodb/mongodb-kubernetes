#!/usr/bin/env bash

set -Eeou pipefail

scripts/dev/run_python.sh -m pytest --junitxml=python-ssdlc-result.xml generate_ssdlc_report_test.py
