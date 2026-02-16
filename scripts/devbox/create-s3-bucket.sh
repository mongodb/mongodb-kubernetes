#!/usr/bin/env bash
# create-s3-bucket.sh
#
# One-time setup: Create the S3 bucket for the Nix binary cache with proper
# configuration for both Evergreen CI and local developer access.
#
# This creates a bucket with:
#   - Server-side encryption (AES-256)
#   - Lifecycle policy to expire old narinfo/nar files after 90 days
#   - Bucket policy allowing read access from Evergreen worker IAM roles
#   - Versioning disabled (cache contents are immutable by hash)
#
# Prerequisites:
#   - AWS CLI configured with admin credentials
#   - Permission to create S3 buckets and IAM policies
#
# Usage:
#   ./scripts/devbox/create-s3-bucket.sh
#
# Environment:
#   MCK_NIX_CACHE_BUCKET           - Bucket name (default: mck-nix-cache)
#   MCK_NIX_CACHE_REGION           - AWS region (default: us-east-1)
#   MCK_EVG_WORKER_ROLE_ARN        - IAM role ARN for Evergreen workers (for bucket policy)
#   MCK_DEVELOPER_ROLE_ARN         - IAM role ARN for developers (for bucket policy)
#
set -Eeou pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/nix-cache.env"

BUCKET="${MCK_NIX_CACHE_BUCKET}"
REGION="${MCK_NIX_CACHE_REGION}"

# These should be set to your real ARNs
EVG_ROLE_ARN="${MCK_EVG_WORKER_ROLE_ARN:-}"
DEV_ROLE_ARN="${MCK_DEVELOPER_ROLE_ARN:-}"

echo "=== MCK Nix Cache S3 Bucket Setup ==="
echo "  Bucket: ${BUCKET}"
echo "  Region: ${REGION}"
echo ""

# --- Create the bucket ---

echo "Step 1/4: Creating S3 bucket..."

if aws s3api head-bucket --bucket "${BUCKET}" --region "${REGION}" 2>/dev/null; then
    echo "  Bucket already exists."
else
    # In us-east-1, LocationConstraint must be omitted
    if [[ "${REGION}" == "us-east-1" ]]; then
        aws s3api create-bucket \
            --bucket "${BUCKET}" \
            --region "${REGION}"
    else
        aws s3api create-bucket \
            --bucket "${BUCKET}" \
            --region "${REGION}" \
            --create-bucket-configuration LocationConstraint="${REGION}"
    fi
    echo "  Bucket created."
fi

echo ""

# --- Enable server-side encryption ---

echo "Step 2/4: Configuring encryption..."

aws s3api put-bucket-encryption \
    --bucket "${BUCKET}" \
    --region "${REGION}" \
    --server-side-encryption-configuration '{
        "Rules": [{
            "ApplyServerSideEncryptionByDefault": {
                "SSEAlgorithm": "AES256"
            },
            "BucketKeyEnabled": true
        }]
    }'

echo "  AES-256 encryption enabled."
echo ""

# --- Set lifecycle policy ---

echo "Step 3/4: Configuring lifecycle policy..."

aws s3api put-bucket-lifecycle-configuration \
    --bucket "${BUCKET}" \
    --region "${REGION}" \
    --lifecycle-configuration '{
        "Rules": [{
            "ID": "expire-old-cache-entries",
            "Status": "Enabled",
            "Filter": {},
            "Expiration": {
                "Days": 90
            },
            "NoncurrentVersionExpiration": {
                "NoncurrentDays": 7
            }
        }]
    }'

echo "  Objects expire after 90 days."
echo ""

# --- Set bucket policy ---

echo "Step 4/4: Configuring bucket policy..."

if [[ -z "${EVG_ROLE_ARN}" ]] && [[ -z "${DEV_ROLE_ARN}" ]]; then
    echo "  WARNING: No IAM role ARNs provided."
    echo "  Set MCK_EVG_WORKER_ROLE_ARN and/or MCK_DEVELOPER_ROLE_ARN"
    echo "  to restrict bucket access to specific IAM roles."
    echo ""
    echo "  Skipping bucket policy (bucket uses default IAM permissions)."
else
    # Build the principal list
    PRINCIPALS=""
    if [[ -n "${EVG_ROLE_ARN}" ]]; then
        PRINCIPALS="\"${EVG_ROLE_ARN}\""
    fi
    if [[ -n "${DEV_ROLE_ARN}" ]]; then
        if [[ -n "${PRINCIPALS}" ]]; then
            PRINCIPALS="${PRINCIPALS}, "
        fi
        PRINCIPALS="${PRINCIPALS}\"${DEV_ROLE_ARN}\""
    fi

    BUCKET_POLICY=$(cat <<POLICY_EOF
{
    "Version": "2012-10-17",
    "Statement": [
        {
            "Sid": "AllowNixCacheRead",
            "Effect": "Allow",
            "Principal": {
                "AWS": [${PRINCIPALS}]
            },
            "Action": [
                "s3:GetObject",
                "s3:ListBucket"
            ],
            "Resource": [
                "arn:aws:s3:::${BUCKET}",
                "arn:aws:s3:::${BUCKET}/*"
            ]
        },
        {
            "Sid": "AllowNixCacheWrite",
            "Effect": "Allow",
            "Principal": {
                "AWS": [${PRINCIPALS}]
            },
            "Action": [
                "s3:PutObject"
            ],
            "Resource": [
                "arn:aws:s3:::${BUCKET}/*"
            ]
        }
    ]
}
POLICY_EOF
)

    aws s3api put-bucket-policy \
        --bucket "${BUCKET}" \
        --region "${REGION}" \
        --policy "${BUCKET_POLICY}"

    echo "  Bucket policy applied."
fi

echo ""

# --- Block public access ---

echo "Ensuring public access is blocked..."

aws s3api put-public-access-block \
    --bucket "${BUCKET}" \
    --region "${REGION}" \
    --public-access-block-configuration \
        "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"

echo "  Public access blocked."
echo ""

# --- Summary ---

echo "=== S3 Bucket Setup Complete ==="
echo ""
echo "Bucket: s3://${BUCKET} (${REGION})"
echo "Encryption: AES-256"
echo "Lifecycle: 90 day expiration"
echo "Public access: blocked"
echo ""
echo "=== Required IAM permissions for consumers ==="
echo ""
echo "Read-only (Evergreen workers, developers pulling cache):"
echo "  s3:GetObject  on arn:aws:s3:::${BUCKET}/*"
echo "  s3:ListBucket on arn:aws:s3:::${BUCKET}"
echo ""
echo "Read-write (CI push job):"
echo "  s3:GetObject  on arn:aws:s3:::${BUCKET}/*"
echo "  s3:PutObject  on arn:aws:s3:::${BUCKET}/*"
echo "  s3:ListBucket on arn:aws:s3:::${BUCKET}"
echo ""
echo "Next steps:"
echo "  1. Generate signing keys: ./scripts/devbox/generate-cache-keys.sh"
echo "  2. Push initial cache:"
echo "     export MCK_NIX_CACHE_PRIV_KEY=\"\$(cat /path/to/nix-cache-priv-key.pem)\""
echo "     ./scripts/devbox/push-cache.sh"
echo "  3. Configure consumers:   ./scripts/devbox/configure-nix-cache.sh"
