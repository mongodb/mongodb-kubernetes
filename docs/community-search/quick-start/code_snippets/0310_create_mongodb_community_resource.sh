kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: mdbc-rs
spec:
  version: 8.0.6
  type: ReplicaSet
  members: 3
  security:
    authentication:
      ignoreUnknownUsers: true
      modes:
        - SCRAM
  agent:
    logLevel: INFO
  statefulSet:
    spec:
      template:
        spec:
          containers:
            - name: mongod
              resources:
                limits:
                  cpu: "3"
                  memory: 5Gi
                requests:
                  cpu: "2"
                  memory: 5Gi
            - name: mongodb-agent
              resources:
                limits:
                  cpu: "2"
                  memory: 5Gi
                requests:
                  cpu: "1"
                  memory: 5Gi
  users:
    - name: admin-user
      passwordSecretRef:
        name: admin-user-password
      roles:
        - db: admin
          name: clusterAdmin
        - db: admin
          name: userAdminAnyDatabase
      scramCredentialsSecretName: admin-user
    - name: search-user
      passwordSecretRef:
        name: search-user-password
      roles:
        - db: sample_mflix
          name: dbOwner
      scramCredentialsSecretName: search-user
EOF
