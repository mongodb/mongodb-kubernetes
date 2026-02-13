# Wait for all search indexes (text and vector) to be ready
# For sharded clusters, the indexes need to sync data from all shards which can take longer
# Using a simple 3-minute wait to ensure all indexes are fully synced and queryable

echo "Waiting 3 minutes for all search indexes to sync across all shards..."
echo "This includes:"
echo "  - Text search index on 'movies' collection"
echo "  - Vector search index on 'embedded_movies' collection"

sleep 180

echo "✓ Wait complete. All search indexes should now be ready."
