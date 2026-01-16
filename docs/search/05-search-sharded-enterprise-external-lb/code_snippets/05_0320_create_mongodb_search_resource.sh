# Create MongoDBSearch resource with External LB configuration for sharded cluster
#
# For sharded clusters with external L7 LB, we configure per-shard endpoints.
# In this PoC, we use the internal K8s service URLs as the "external" endpoints
# since we're not deploying a real L7 load balancer.
#
# The endpoints point to the per-shard mongot services that the operator creates:
# - <search-name>-mongot-<shard-name>-svc.<namespace>.svc.cluster.local:27028

# Build the endpoints array dynamically based on shard count
endpoints_yaml=""
for i in $(seq 0 $((MDB_SHARD_COUNT - 1))); do
  shard_name="${MDB_RESOURCE_NAME}-${i}"
  endpoint="${MDB_RESOURCE_NAME}-mongot-${shard_name}-svc.${MDB_NS}.svc.cluster.local:27028"
  endpoints_yaml="${endpoints_yaml}
          - shardName: ${shard_name}
            endpoint: ${endpoint}"
done

kubectl apply --context "${K8S_CTX}" -n "${MDB_NS}" -f - <<EOF
apiVersion: mongodb.com/v1
kind: MongoDBSearch
metadata:
  name: ${MDB_RESOURCE_NAME}
spec:
  logLevel: DEBUG
  lb:
    mode: External
    external:
      sharded:
        endpoints:${endpoints_yaml}
  resourceRequirements:
    limits:
      cpu: "2"
      memory: 3Gi
    requests:
      cpu: "1"
      memory: 2Gi
EOF

