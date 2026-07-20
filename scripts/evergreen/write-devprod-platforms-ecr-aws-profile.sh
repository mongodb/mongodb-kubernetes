#!/usr/bin/env bash

# Writes the credentials assumed via the preceding ec2.assume_role step to a named AWS profile,
# rather than exporting them as bare AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_SESSION_TOKEN env
# vars. Other AWS calls made later in the same task (e.g. ecr_login_boto3 in
# scripts/release/build/image_build_process.py, which authenticates to a different ECR account)
# rely on ambient/instance credentials via the default credential chain, so the assumed-role
# credentials must be scoped to a named profile instead of polluting that chain.
#
# This script is the sole owner of the "devprod-platforms-ecr" section in these files, and is
# written to be safe to run more than once against the same files (e.g. if a task retries on a
# reused host): it removes any block it previously wrote before appending the current one, rather
# than appending unconditionally, which would otherwise leave a duplicate "[profile ...]" section
# that botocore's config parser rejects outright, breaking every AWS call in the task.

set -euo pipefail

: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID must be set}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY must be set}"
: "${AWS_SESSION_TOKEN:?AWS_SESSION_TOKEN must be set}"

mkdir -p ~/.aws
touch ~/.aws/credentials ~/.aws/config

# Removes any existing "[section_header]" block (up to the next "[" line or EOF) so re-running
# this script is idempotent instead of accumulating duplicate sections.
remove_existing_section() {
    local file="$1"
    local section_header="$2"
    awk -v header="$section_header" '
        $0 == header { in_section = 1; next }
        /^\[/ { in_section = 0 }
        !in_section { print }
    ' "$file" >"${file}.tmp"
    mv "${file}.tmp" "$file"
}

remove_existing_section ~/.aws/credentials "[devprod-platforms-ecr]"
cat >>~/.aws/credentials <<EOF
[devprod-platforms-ecr]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}
aws_session_token = ${AWS_SESSION_TOKEN}
EOF

remove_existing_section ~/.aws/config "[profile devprod-platforms-ecr]"
cat >>~/.aws/config <<EOF
[profile devprod-platforms-ecr]
region = us-east-1
EOF
