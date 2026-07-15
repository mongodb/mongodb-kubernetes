echo "Creating the source CA ConfigMap '${SOURCE_CA_CONFIGMAP}' in every member cluster..."
echo "spec.source.external.tls.ca requires the ca.crt key specifically -- ra-05's"
echo "'ca-issuer' ConfigMap (ca-pem/mms-ca.crt keys) does not carry it, so we create our own."

ca_tmp="/tmp/search-source-ca.pem"

kubectl get secret "${MDB_TLS_CA_SECRET_NAME}" -n "${CERT_MANAGER_NAMESPACE}" \
  --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -o jsonpath='{.data.tls\.crt}' | base64 -d > "${ca_tmp}"

for ctx in "${K8S_CLUSTER_0_CONTEXT_NAME}" "${K8S_CLUSTER_1_CONTEXT_NAME}" "${K8S_CLUSTER_2_CONTEXT_NAME}"; do
  kubectl create configmap "${SOURCE_CA_CONFIGMAP}" \
    --from-file=ca.crt="${ca_tmp}" \
    -n "${MDB_NAMESPACE}" \
    --context "${ctx}" \
    --dry-run=client -o yaml | kubectl apply --context "${ctx}" -f -

  echo "  [ok] ConfigMap '${SOURCE_CA_CONFIGMAP}' created in ${ctx}"
done

rm -f "${ca_tmp}"
