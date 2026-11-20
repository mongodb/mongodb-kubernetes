# The management Ops Manager owns the project the AppDB CR is registered under.
# It runs its own internally-managed AppDB (this is a normal Ops Manager deployment).
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${MANAGEMENT_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  applicationDatabase:
    members: 3
    version: ${APPDB_VERSION}
  backup:
    enabled: false
  configuration:
    automation.versions.source: mongodb
    mms.ignoreInitialUiSetup: "true"
    # OM preflight (with ignoreInitialUiSetup) requires these mail settings to be set.
    mms.adminEmailAddr: cloud-manager-support@mongodb.com
    mms.fromEmailAddr: cloud-manager-support@mongodb.com
    mms.replyToEmailAddr: cloud-manager-support@mongodb.com
    mms.mail.hostname: email-smtp.us-east-1.amazonaws.com
    mms.mail.port: "465"
    mms.mail.ssl: "true"
    mms.mail.transport: smtp
    mms.minimumTLSVersion: TLSv1.2
EOF

echo "[ok] Management Ops Manager '${MANAGEMENT_OM_NAME}' created"
