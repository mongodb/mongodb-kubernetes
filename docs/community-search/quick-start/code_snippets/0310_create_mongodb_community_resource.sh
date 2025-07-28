kubectl apply --context "${K8S_CLUSTER_0_CONTEXT_NAME}" -n "${MDB_NAMESPACE}" -f - <<EOF
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: mdbc-rs
spec:
  version: ${MDB_VERSION}
  type: ReplicaSet
  members: 3
  security:
    authentication:
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
            - name: mongod
              resources:
                limits:
                  cpu: "2"
                  memory: 2Gi
                requests:
                  cpu: "1"
                  memory: 1Gi
            - name: mongodb-agent
              resources:
                limits:
                  cpu: "1"
                  memory: 2Gi
                requests:
                  cpu: "0.5"
                  memory: 1Gi
  users:
    # admin user with root role
    - name: mdb-admin
      db: admin
      passwordSecretRef: # a reference to the secret containing user password
        name: mdb-admin-user-password
      scramCredentialsSecretName: mdb-admin-user-scram
      roles:
        - name: root
          db: admin
    # user performing search queries
    - name: mdb-user
      db: admin
      passwordSecretRef: # a reference to the secret containing user password
        name: mdb-user-password
      scramCredentialsSecretName: mdb-user-scram
      roles:
        - name: restore
          db: sample_mflix
        - name: readWrite
          db: sample_mflix
    # user used by MongoDB Search to connect to MongoDB database to synchronize data from
    # For MongoDB <8.2, the operator will be creating the searchCoordinator custom role automatically
    # From MongoDB 8.2, searchCoordinator role will be a built-in role.
    - name: search-sync-source
      db: admin
      passwordSecretRef: # a reference to the secret containing user password
        name: mdbc-rs-search-sync-source-password
      scramCredentialsSecretName: mdb-rs-search-sync-source
      roles:
        - name: searchCoordinator
          db: admin
EOF
