# Start from an Ops Manager with an internally-managed MULTI-CLUSTER AppDB (spec.applicationDatabase
# with topology: MultiCluster). This is the "before" state we migrate away from. The internal AppDB
# must be MultiCluster so the later switch to an external MongoDBMultiCluster is allowed (the operator
# requires the current and external AppDB topologies to match). The OM instance runs on the central
# cluster; its AppDB members are spread across the member clusters and are TLS-enabled.
kubectl apply --context "${K8S_CTX_0}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBOpsManager
metadata:
  name: ${PRIMARY_OM_NAME}
spec:
  replicas: 1
  version: ${OM_VERSION}
  adminCredentials: ops-manager-admin-secret
  topology: MultiCluster
  clusterSpecList:
    - clusterName: ${K8S_CTX_0}
      members: 1
  applicationDatabase:
    topology: MultiCluster
    version: ${APPDB_VERSION}
    security:
      certsSecretPrefix: ${APPDB_CERT_PREFIX}
      tls:
        ca: ${APPDB_CA_CONFIGMAP}
    clusterSpecList:
      - clusterName: ${K8S_CTX_1}
        members: ${APPDB_MEMBERS_C1}
      - clusterName: ${K8S_CTX_2}
        members: ${APPDB_MEMBERS_C2}
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

echo "[ok] Primary Ops Manager '${PRIMARY_OM_NAME}' created with an internal multi-cluster AppDB"
