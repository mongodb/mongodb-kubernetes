kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
---
apiVersion: mongodb.com/v1
kind: MongoDB
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  members: 3
  version: ${MDB_VERSION}
  type: ReplicaSet
  opsManager:
    configMapRef:
      name: ${MDB_OPS_MANAGER_CONFIG_MAP_NAME}
  credentials: ${MDB_OPS_MANAGER_CREDENTIALS_SECRET_NAME}
  security:
    authentication:
      enabled: true
      ignoreUnknownUsers: true
      modes:
      - SCRAM
  agent:
    logLevel: DEBUG
  statefulSet:
    spec:
      template:
        spec:
          containers:
          - name: mongodb-enterprise-database
            resources:
              limits:
                cpu: "2"
                memory: 2Gi
              requests:
                cpu: "1"
                memory: 1Gi
EOF
