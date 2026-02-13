# Wait for search indexes to be ready
# For sharded clusters with external MongoDB, the initial sync can take a while
# We use a simple sleep approach to allow indexes to sync data from all shards

echo "Waiting 10 minutes for search indexes to sync data across all shards..."
echo "This allows time for mongot to index data from all shards."

sleep 600

echo "✓ Wait complete, proceeding with search queries"
