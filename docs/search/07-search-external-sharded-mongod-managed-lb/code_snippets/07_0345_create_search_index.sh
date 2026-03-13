#!/usr/bin/env bash
# Create a text search index on the movies collection
#
# Search indexes are created through mongos and automatically distributed
# to each shard's mongot instance via the Envoy proxy.
#
# This creates a default text search index that indexes all text fields.

echo "Creating search index on sample_mflix.movies..."

# Connection string for user operations
user_conn="mongodb://mdb-user:${MDB_USER_PASSWORD}@${MDB_EXTERNAL_CLUSTER_NAME}-mongos-0.${MDB_EXTERNAL_CLUSTER_NAME}-svc.${MDB_NS}.svc.cluster.local:27017/?tls=true&tlsCAFile=/tls/ca-pem&authSource=admin"

kubectl exec mongodb-tools -n "${MDB_NS}" --context "${K8S_CTX}" -- mongosh "${user_conn}" --quiet --eval '
  use sample_mflix;
  
  // Check if index already exists
  const existing = db.movies.aggregate([
    { $listSearchIndexes: {} }
  ]).toArray();
  
  if (existing.some(idx => idx.name === "default")) {
    print("Search index 'default' already exists");
  } else {
    // Create default text search index
    db.movies.createSearchIndex({
      name: "default",
      definition: {
        mappings: {
          dynamic: true
        }
      }
    });
    print("Search index 'default' created");
  }
  
  print("\nNote: Search index may take a few minutes to build and sync.");
'

echo "✓ Search index creation initiated"

