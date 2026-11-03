# Register each member cluster with the operator on the central cluster, now that the
# helm install has created the MemberCluster CRD. The logical name
# (--member-cluster-logical-name) is what clusterSpecList[].clusterName refers to;
# kind contexts contain hyphens and are already RFC 1123 compliant, but pass it
# explicitly so the MemberCluster CR's spec.clusterName matches the context name.
echo "Registering member clusters with the operator..."

for ctx in "${K8S_CTX_0}" "${K8S_CTX_1}" "${K8S_CTX_2}"; do
  kubectl mongodb multicluster generate-member-registration \
    --member-cluster="${ctx}" \
    --member-cluster-context="${ctx}" \
    --member-cluster-namespace="${MDB_NS}" \
    --operator-namespace="${MDB_NS}" \
    --member-cluster-logical-name="${ctx}" \
    | kubectl apply --context "${K8S_CTX_0}" -f -
done

echo "[ok] Member clusters registered"
