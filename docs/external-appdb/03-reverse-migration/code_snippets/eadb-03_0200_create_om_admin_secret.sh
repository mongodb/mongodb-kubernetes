# Global admin user created on first startup of each Ops Manager instance.
# Both the management OM and the primary OM reference this secret via spec.adminCredentials.
kubectl create secret generic ops-manager-admin-secret \
  --context "${K8S_CTX}" -n "${MDB_NS}" \
  --from-literal=Username="${OM_ADMIN_EMAIL}" \
  --from-literal=Password="${OM_ADMIN_PASSWORD}" \
  --from-literal=FirstName="${OM_ADMIN_FIRST_NAME}" \
  --from-literal=LastName="${OM_ADMIN_LAST_NAME}" \
  --dry-run=client -o yaml \
  | kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f -

echo "[ok] Ops Manager admin secret 'ops-manager-admin-secret' ready"
