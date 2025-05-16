#!/bin/bash

kubectl exec -n "${MDB_NAMESPACE}" --context "${K8S_CLUSTER_0_CONTEXT_NAME}" mongodb-tools-pod -- /bin/bash -eu -c "$(cat <<EOF
echo "Downloading sample database archive..."
curl https://atlas-education.s3.amazonaws.com/sample_mflix.archive -o /tmp/sample_mflix.archive
echo "Restoring sample database"
mongorestore --archive=/tmp/sample_mflix.archive --verbose=1 --drop --nsInclude 'sample_mflix.*' --uri="mongodb://search-user:${MDB_SEARCH_USER_PASSWORD}@mdbc-rs-0.mdbc-rs-svc.${MDB_NAMESPACE}.svc.cluster.local:27017/?replicaSet=mdbc-rs"
EOF
)"
