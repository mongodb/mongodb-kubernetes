# Wait for vector search index to be ready
# For sharded clusters with external MongoDB, the initial sync can take a while
# We use a simple sleep approach since checking index status is unreliable

echo "Waiting for vector search index to sync data..."
echo "This may take several minutes for sharded clusters..."

# Sleep for 3 minutes to allow initial sync to complete
sleep 180

echo "✓ Wait complete, proceeding with vector search queries"
