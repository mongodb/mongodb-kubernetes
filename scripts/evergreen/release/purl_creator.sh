#!/bin/bash

set -euo pipefail

###
# This is an internal script which is part of the sbom.py file. Do not call it directly.
###

go version -m "$1" | sed -E -n 's% *(dep|=>) *([^ ]*) *([^ ]*)( .*)?%pkg:golang/\2@\3%p' > "$2"
