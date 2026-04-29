echo "Smoke-test the per-cluster, per-shard Envoy SNI endpoints..."
echo ""
echo "Each (cluster, shard) tuple has its own SNI hostname:"
echo "  ${MEMBER_CLUSTER_0_NAME}.${MDB_SHARD_0_NAME}.search-lb.lt.example.com:443"
echo "  ${MEMBER_CLUSTER_0_NAME}.${MDB_SHARD_1_NAME}.search-lb.lt.example.com:443"
echo "  ${MEMBER_CLUSTER_1_NAME}.${MDB_SHARD_0_NAME}.search-lb.lt.example.com:443"
echo "  ${MEMBER_CLUSTER_1_NAME}.${MDB_SHARD_1_NAME}.search-lb.lt.example.com:443"
echo ""
echo "Connect mongosh to your external mongos and run a search query; mongot lookups"
echo "happen server-side and pin to the same-region cluster's Envoy per shard."
echo ""
cat <<'EOF'
mongosh "mongodb://search-sync-source@<external-mongos>:27017/?tls=true" \
  --eval 'db.getSiblingDB("admin").runCommand({ ping: 1 })'
EOF
echo ""
echo "Per-(cluster,shard) reachability check:"
cat <<'EOF'
openssl s_client -connect <clusterName>.<shardName>.search-lb.lt.example.com:443 \
  -servername <clusterName>.<shardName>.search-lb.lt.example.com -showcerts < /dev/null
EOF
