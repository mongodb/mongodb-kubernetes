echo "Smoke-test the per-cluster Envoy SNI endpoint with mongosh..."
echo ""
echo "Each member cluster publishes its own Envoy frontend at:"
echo "  ${MEMBER_CLUSTER_0_NAME}.search-lb.lt.example.com:443"
echo "  ${MEMBER_CLUSTER_1_NAME}.search-lb.lt.example.com:443"
echo ""
echo "From a host with mongosh + DNS access, run a search query against the external mongod"
echo "via mongos / your test client; mongot connections will pin to the same-region cluster's"
echo "endpoint per spec §5.1.2."
echo ""
echo "Example (run against the external replica set; mongot lookups happen server-side):"
cat <<'EOF'
mongosh "mongodb://search-sync-source@<external-mongod-host>:27017/?tls=true" \
  --eval 'db.getSiblingDB("admin").runCommand({ ping: 1 })'
EOF
echo ""
echo "Verify the per-cluster Envoy is reachable on its SNI hostname:"
cat <<'EOF'
openssl s_client -connect <clusterName>.search-lb.lt.example.com:443 \
  -servername <clusterName>.search-lb.lt.example.com -showcerts < /dev/null
EOF
