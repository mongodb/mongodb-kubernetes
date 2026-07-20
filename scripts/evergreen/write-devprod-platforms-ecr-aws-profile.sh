#!/usr/bin/env bash

# Writes the credentials assumed via the "assume Silkbomb ECR readonly role"-equivalent
# ec2.assume_role step to a named AWS profile, rather than exporting them as bare
# AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_SESSION_TOKEN env vars. Other AWS calls made later in
# the same task (e.g. ecr_login_boto3 in scripts/release/build/image_build_process.py, which
# authenticates to a different ECR account) rely on ambient/instance credentials via the default
# credential chain, so the assumed-role credentials must be scoped to a named profile instead of
# polluting that chain.

set -euo pipefail

: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID must be set}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY must be set}"
: "${AWS_SESSION_TOKEN:?AWS_SESSION_TOKEN must be set}"

mkdir -p ~/.aws

cat >>~/.aws/credentials <<EOF
[devprod-platforms-ecr]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}
aws_session_token = ${AWS_SESSION_TOKEN}
EOF

cat >>~/.aws/config <<EOF
[profile devprod-platforms-ecr]
region = us-east-1
EOF
