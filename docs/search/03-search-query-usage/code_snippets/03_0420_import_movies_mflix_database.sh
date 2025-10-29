# Restore sample_mflix database with TLS (auto-detect modern vs legacy flags)
kubectl exec -n "${MDB_NS}" --context "${K8S_CTX}" \
  mongodb-tools-pod -- env MDB_CONNECTION_STRING="${MDB_CONNECTION_STRING}" /bin/bash -eu -c "$(cat <<'EOF'
set -o pipefail
echo "Downloading sample database archive..."
curl -fSL https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive

echo "Detecting mongorestore TLS flag support..."
if mongorestore --help 2>&1 | grep -q -- '--tlsCAFile'; then
  TLS_FLAG="--tls"
  CA_FLAG="--tlsCAFile"
else
  TLS_FLAG="--ssl"
  CA_FLAG="--sslCAFile"
fi

echo "Using $TLS_FLAG with $CA_FLAG"

echo "Restoring sample database with TLS"
mongorestore \
  --archive=/tmp/sample_mflix.archive \
  --verbose=1 \
  --drop \
  --nsInclude 'sample_mflix.*' \
  "$TLS_FLAG" \
  "$CA_FLAG" /tls/ca.crt \
  --uri="${MDB_CONNECTION_STRING}"
EOF
)"
