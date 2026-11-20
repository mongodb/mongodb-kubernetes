# The primary Ops Manager. It has NO spec.applicationDatabase — instead it references the external
# AppDB MongoDB CR created above. The operator resolves that CR for the connection string, TLS/CA and
# never manages an internal AppDB StatefulSet for this OM.
kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${PRIMARY_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  externalApplicationDatabaseRef:
    name: ${APPDB_NAME}
    kind: MongoDB
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

echo "[ok] Primary Ops Manager '${PRIMARY_OM_NAME}' created (externalApplicationDatabaseRef -> ${APPDB_NAME})"
