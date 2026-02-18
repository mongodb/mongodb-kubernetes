kubectl create namespace "${MDB_NS}" --context "${K8S_CTX}" --dry-run=client -o yaml | kubectl apply --context "${K8S_CTX}" -f -

