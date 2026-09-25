# Defaults for the S3-compatible storage used by the Ops Manager backup
# snippets. Override any of these before running this module to point the
# architecture tests at your own S3 storage.
export RUSTFS_NAMESPACE="${RUSTFS_NAMESPACE:-rustfs}"
export S3_ENDPOINT="${S3_ENDPOINT:-rustfs.${RUSTFS_NAMESPACE}.svc.cluster.local}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-rustfsadmin}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-rustfsadmin123}"
export S3_OPLOG_BUCKET_NAME="${S3_OPLOG_BUCKET_NAME:-s3-oplog-store}"
export S3_SNAPSHOT_BUCKET_NAME="${S3_SNAPSHOT_BUCKET_NAME:-s3-snapshot-store}"
